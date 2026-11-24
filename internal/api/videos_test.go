package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/api"
)

// mp4 devolve bytes que começam como um MP4 (caixa ftyp) e têm o tamanho dado.
func mp4(tamanho int) []byte {
	b := make([]byte, tamanho)
	copy(b, "\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	return b
}

type arquivo struct {
	campo, nome string
	conteudo    []byte
}

// multipartDe monta o corpo multipart com os arquivos dados e o seu tipo.
func multipartDe(arquivos ...arquivo) (tipo string, corpo io.Reader) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for _, a := range arquivos {
		parte, _ := w.CreateFormFile(a.campo, a.nome) // só falha se b falhar
		_, _ = parte.Write(a.conteudo)
	}
	_ = w.Close()
	return w.FormDataContentType(), &b
}

// novoEnvio monta um POST /videos autenticado.
func novoEnvio(ctx context.Context, base, token, tipo string, corpo io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/videos", corpo)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", tipo)
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

// executar faz a requisição e lê a resposta; não chama t.Fatal, então serve
// também em goroutines.
func executar(cli *http.Client, req *http.Request) (resposta, error) {
	res, err := cli.Do(req)
	if err != nil {
		return resposta{}, err
	}
	defer func() { _ = res.Body.Close() }()
	r := resposta{status: res.StatusCode, header: res.Header}
	b, _ := io.ReadAll(res.Body)
	if len(b) > 0 {
		_ = json.Unmarshal(b, &r.corpo)
	}
	return r, nil
}

// enviar manda um multipart com os arquivos dados para POST /videos.
func (c cliente) enviar(token string, arquivos ...arquivo) resposta {
	c.t.Helper()
	tipo, corpo := multipartDe(arquivos...)
	return c.enviarCorpo(token, tipo, corpo)
}

func (c cliente) enviarCorpo(token, tipo string, corpo io.Reader) resposta {
	c.t.Helper()
	req, err := novoEnvio(c.t.Context(), c.srv.URL, token, tipo, corpo)
	if err != nil {
		c.t.Fatalf("nova requisição: %v", err)
	}
	r, err := executar(c.srv.Client(), req)
	if err != nil {
		c.t.Fatalf("POST /videos: %v", err)
	}
	return r
}

func (c cliente) enviarVideo(token, nome string) string {
	c.t.Helper()
	r := c.enviar(token, arquivo{"video", nome, mp4(4096)})
	id, _ := r.corpo["id"].(string)
	if r.status != http.StatusAccepted || id == "" {
		c.t.Fatalf("enviar %s: status %d corpo %v", nome, r.status, r.corpo)
	}
	return id
}

// arquivosNoVolume lista os arquivos regulares sob o diretório de dados.
func (c cliente) arquivosNoVolume() []string {
	c.t.Helper()
	var achados []string
	_ = filepath.WalkDir(c.dados, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			achados = append(achados, p)
		}
		return nil
	})
	return achados
}

func (c cliente) listar(token, rota string) (ids []string, proximo string, r resposta) {
	c.t.Helper()
	r = c.fazer(http.MethodGet, rota, token, nil)
	lista, _ := r.corpo["videos"].([]any)
	for _, item := range lista {
		v, _ := item.(map[string]any)
		id, _ := v["id"].(string)
		ids = append(ids, id)
	}
	proximo, _ = r.corpo["proximo_cursor"].(string)
	return ids, proximo, r
}

// exigirNadaCriado confere que o envio recusado não deixou Vídeo nem arquivo.
func exigirNadaCriado(t *testing.T, c cliente, token string) {
	t.Helper()
	if ids, _, _ := c.listar(token, "/videos"); len(ids) != 0 {
		t.Errorf("Vídeos criados: %v, quero nenhum", ids)
	}
	if arqs := c.arquivosNoVolume(); len(arqs) != 0 {
		t.Errorf("arquivos no volume: %v, quero nenhum", arqs)
	}
}

func TestEnvio_VideoValidoDa202ComOriginalNoVolumeEOutbox(t *testing.T) {
	c, pool := subirAPI(t)
	token := c.usuario("ana@ex.com")

	r := c.enviar(token, arquivo{"video", "férias.mp4", mp4(4096)})
	if r.status != http.StatusAccepted {
		t.Fatalf("status = %d, quero 202 (corpo %v)", r.status, r.corpo)
	}
	id, _ := r.corpo["id"].(string)
	if r.corpo["status"] != "recebido" {
		t.Errorf("status do Vídeo = %v, quero recebido", r.corpo["status"])
	}
	if got := r.header.Get("Location"); got != "/videos/"+id {
		t.Errorf("Location = %q, quero /videos/%s", got, id)
	}

	original, err := fs.ReadFile(os.DirFS(c.dados), "videos/"+id+"/original")
	if err != nil {
		t.Fatalf("ler original: %v", err)
	}
	if !bytes.Equal(original, mp4(4096)) {
		t.Error("o original gravado difere do enviado")
	}

	// O critério de aceite é sobre a outbox, que a API não expõe.
	var pendentes int
	err = pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox
		WHERE publicado_em IS NULL AND routing_key = 'processamento' AND payload->>'video_id' = $1
		AND payload->>'correlation_id' <> ''`, id).Scan(&pendentes)
	if err != nil || pendentes != 1 {
		t.Errorf("linhas pendentes na outbox para o Vídeo = %d (%v), quero 1", pendentes, err)
	}

	v := c.fazer(http.MethodGet, "/videos/"+id, token, nil)
	if v.status != http.StatusOK || v.corpo["nome_original"] != "férias.mp4" || v.corpo["status"] != "recebido" {
		t.Errorf("GET /videos/%s: status %d corpo %v", id, v.status, v.corpo)
	}
	for _, campo := range []string{"motivo", "download", "zip_expira_em"} {
		if _, ok := v.corpo[campo]; ok {
			t.Errorf("Vídeo recebido não deveria ter %q", campo)
		}
	}
}

func TestEnvio_NomeOriginalSemSeparadoresNemControle(t *testing.T) {
	c, _ := subirAPI(t)
	token := c.usuario("ana@ex.com")
	// U+202E (RLO) inverte o texto exibido e serviria para disfarçar a extensão.
	id := c.enviarVideo(token, "a\tb\\c:d\u202e.mp4")

	v := c.fazer(http.MethodGet, "/videos/"+id, token, nil)
	if v.corpo["nome_original"] != "ab_c_d.mp4" {
		t.Errorf("nome_original = %q, quero %q", v.corpo["nome_original"], "ab_c_d.mp4")
	}
}

func TestEnvio_NaoVideoDa415SemCriarNada(t *testing.T) {
	c, _ := subirAPI(t)
	token := c.usuario("ana@ex.com")
	pdf := append([]byte("%PDF-1.7\n"), make([]byte, 4096)...)

	if r := c.enviar(token, arquivo{"video", "relatorio.mp4", pdf}); r.status != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, quero 415 (corpo %v)", r.status, r.corpo)
	}
	exigirNadaCriado(t, c, token)
}

func TestEnvio_AcimaDoTetoDa413ApagaOTemporario(t *testing.T) {
	c, _ := subirAPI(t, func(cfg *api.Config) { cfg.TamanhoMaxEnvio = 1 << 20 })
	token := c.usuario("ana@ex.com")

	if r := c.enviar(token, arquivo{"video", "grande.mp4", mp4(2 << 20)}); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, quero 413 (corpo %v)", r.status, r.corpo)
	}
	exigirNadaCriado(t, c, token)
}

func TestEnvio_ParadoAlemDoPrazoDa408SemCriarNada(t *testing.T) {
	c, _ := subirAPI(t, func(cfg *api.Config) { cfg.TimeoutEnvio = 300 * time.Millisecond })
	token := c.usuario("ana@ex.com")

	// O cliente manda o começo do vídeo e para de enviar sem fechar o corpo.
	leitor, escritor := io.Pipe()
	t.Cleanup(func() { _ = escritor.Close() })
	w := multipart.NewWriter(escritor)
	go func() {
		parte, _ := w.CreateFormFile("video", "lento.mp4")
		_, _ = parte.Write(mp4(4096))
	}()

	if r := c.enviarCorpo(token, w.FormDataContentType(), leitor); r.status != http.StatusRequestTimeout {
		t.Fatalf("status = %d, quero 408", r.status)
	}
	exigirNadaCriado(t, c, token)
}

func TestEnvio_DoisArquivosDa400SemCriarNada(t *testing.T) {
	c, _ := subirAPI(t)
	token := c.usuario("ana@ex.com")

	r := c.enviar(token, arquivo{"video", "a.mp4", mp4(4096)}, arquivo{"video", "b.mp4", mp4(4096)})
	if r.status != http.StatusBadRequest {
		t.Fatalf("status = %d, quero 400 (corpo %v)", r.status, r.corpo)
	}
	exigirNadaCriado(t, c, token)
}

func TestEnvio_SemCampoVideoDa400(t *testing.T) {
	c, _ := subirAPI(t)
	token := c.usuario("ana@ex.com")
	if r := c.enviar(token, arquivo{"outro", "a.mp4", mp4(4096)}); r.status != http.StatusBadRequest {
		t.Errorf("status = %d, quero 400", r.status)
	}
	exigirNadaCriado(t, c, token)
}

func TestListagem_SoDoDonoDoMaisRecenteParaOMaisAntigo(t *testing.T) {
	c, _ := subirAPI(t)
	ana, beto := c.usuario("ana@ex.com"), c.usuario("beto@ex.com")

	var daAna []string
	for i := range 3 {
		daAna = append(daAna, c.enviarVideo(ana, fmt.Sprintf("ana%d.mp4", i)))
		c.enviarVideo(beto, fmt.Sprintf("beto%d.mp4", i))
	}

	ids, _, r := c.listar(ana, "/videos")
	if r.status != http.StatusOK {
		t.Fatalf("status = %d, quero 200", r.status)
	}
	quero := []string{daAna[2], daAna[1], daAna[0]}
	if fmt.Sprint(ids) != fmt.Sprint(quero) {
		t.Errorf("ids = %v, quero %v", ids, quero)
	}
}

func TestListagem_PaginaPorCursorSemRepetir(t *testing.T) {
	c, _ := subirAPI(t, func(cfg *api.Config) { cfg.CotaVideos = 25 })
	ana := c.usuario("ana@ex.com")
	enviados := map[string]bool{}
	for i := range 25 {
		enviados[c.enviarVideo(ana, "v"+strconv.Itoa(i)+".mp4")] = true
	}

	primeira, cursor, _ := c.listar(ana, "/videos?limite=20")
	if len(primeira) != 20 || cursor == "" {
		t.Fatalf("primeira página: %d itens, cursor %q; quero 20 e um cursor", len(primeira), cursor)
	}
	segunda, fim, _ := c.listar(ana, "/videos?limite=20&cursor="+cursor)
	if len(segunda) != 5 || fim != "" {
		t.Fatalf("segunda página: %d itens, cursor %q; quero 5 e sem cursor", len(segunda), fim)
	}
	vistos := map[string]bool{}
	for _, id := range append(primeira, segunda...) {
		if vistos[id] || !enviados[id] {
			t.Errorf("Vídeo %s repetido ou desconhecido", id)
		}
		vistos[id] = true
	}
}

func TestListagem_LimiteForaDaFaixaOuCursorInvalidoDa400(t *testing.T) {
	c, _ := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	for _, rota := range []string{"/videos?limite=0", "/videos?limite=101", "/videos?limite=x", "/videos?cursor=lixo"} {
		if _, _, r := c.listar(ana, rota); r.status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, quero 400", rota, r.status)
		}
	}
}

func TestConsulta_VideoAlheioDa404IgualAInexistente(t *testing.T) {
	c, _ := subirAPI(t)
	ana, beto := c.usuario("ana@ex.com"), c.usuario("beto@ex.com")
	doBeto := c.enviarVideo(beto, "beto.mp4")

	for nome, id := range map[string]string{
		"de outro Usuário": doBeto,
		"inexistente":      "00000000-0000-4000-8000-000000000000",
		"id inválido":      "nao-e-uuid",
	} {
		r := c.fazer(http.MethodGet, "/videos/"+id, ana, nil)
		if r.status != http.StatusNotFound || r.corpo["erro"] != "vídeo não encontrado" {
			t.Errorf("%s: status %d corpo %v, quero 404 \"vídeo não encontrado\"", nome, r.status, r.corpo)
		}
	}
}
