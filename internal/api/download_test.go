package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// zipDeTeste faz o papel do Zip de frames: o download não abre o zip, só o
// serve.
var zipDeTeste = append([]byte("PK\x03\x04"), bytes.Repeat([]byte("frames"), 2000)...)

// concluir faz o papel do Worker: publica o zip no volume e marca o Vídeo
// concluido, com o zip expirando em expira.
func (c cliente) concluir(pool *pgxpool.Pool, id string, expira time.Time) {
	c.t.Helper()
	if err := os.WriteFile(filepath.Join(c.dados, "videos", id, "frames.zip"), zipDeTeste, 0o600); err != nil {
		c.t.Fatalf("publicar zip: %v", err)
	}
	if _, err := pool.Exec(c.t.Context(), `UPDATE videos SET status = 'concluido', zip_expira_em = $2 WHERE id = $1`,
		id, expira); err != nil {
		c.t.Fatalf("concluir %s: %v", id, err)
	}
}

type download struct {
	status int
	header http.Header
	corpo  []byte
}

// json devolve o corpo como objeto JSON (as respostas de erro).
func (d download) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(d.corpo, &m); err != nil {
		t.Fatalf("corpo não é JSON (%v): %q", err, d.corpo)
	}
	return m
}

func (c cliente) baixar(token, id string, cabecalhos map[string]string) download {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), http.MethodGet, c.srv.URL+"/videos/"+id+"/zip", nil)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range cabecalhos {
		req.Header.Set(k, v)
	}
	res, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatalf("baixar: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	corpo, err := io.ReadAll(res.Body)
	if err != nil {
		c.t.Fatalf("ler download: %v", err)
	}
	return download{status: res.StatusCode, header: res.Header, corpo: corpo}
}

func TestDownload_ZipPronto(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	c.concluir(pool, id, time.Now().Add(time.Hour))

	d := c.baixar(ana, id, nil)

	if d.status != http.StatusOK {
		t.Fatalf("status = %d, quero 200 (corpo %q)", d.status, d.corpo)
	}
	if got := d.header.Get("Content-Disposition"); got != "attachment; filename=ferias-frames.zip" {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := d.header.Get("Content-Type"); got != "application/zip" {
		t.Errorf("Content-Type = %q, quero application/zip", got)
	}
	if !bytes.Equal(d.corpo, zipDeTeste) {
		t.Errorf("corpo com %d bytes difere do zip publicado (%d bytes)", len(d.corpo), len(zipDeTeste))
	}
	if v := c.fazer(http.MethodGet, "/videos/"+id, ana, nil); v.corpo["download"] != "/videos/"+id+"/zip" {
		t.Errorf("consulta sem o link de download: %v", v.corpo)
	}
	lista := c.fazer(http.MethodGet, "/videos", ana, nil)
	if itens, _ := lista.corpo["videos"].([]any); len(itens) != 1 || itens[0].(map[string]any)["download"] != "/videos/"+id+"/zip" {
		t.Errorf("listagem sem o link de download: %v", lista.corpo)
	}
}

func TestDownload_RetomadoComRange(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	c.concluir(pool, id, time.Now().Add(time.Hour))
	meio := len(zipDeTeste) / 2

	d := c.baixar(ana, id, map[string]string{"Range": "bytes=" + strconv.Itoa(meio) + "-"})

	if d.status != http.StatusPartialContent {
		t.Fatalf("status = %d, quero 206", d.status)
	}
	if !bytes.Equal(d.corpo, zipDeTeste[meio:]) {
		t.Errorf("trecho com %d bytes, quero os %d do meio em diante", len(d.corpo), len(zipDeTeste)-meio)
	}
}

func TestDownload_NomeDoArquivo(t *testing.T) {
	casos := map[string]string{
		"férias.mp4":     "attachment; filename*=utf-8''f%C3%A9rias-frames.zip",
		"video":          "attachment; filename=video-frames.zip",
		"a b;c\"d.mov":   "attachment; filename=\"a b;c\\\"d-frames.zip\"",
		".mp4":           "attachment; filename=video-frames.zip",
		"clip.final.mkv": "attachment; filename=clip.final-frames.zip",
	}
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	for nome, quero := range casos {
		t.Run(nome, func(t *testing.T) {
			id := c.enviarVideo(ana, nome)
			c.concluir(pool, id, time.Now().Add(time.Hour))
			if got := c.baixar(ana, id, nil).header.Get("Content-Disposition"); got != quero {
				t.Errorf("Content-Disposition = %q, quero %q", got, quero)
			}
		})
	}
}

func TestDownload_AindaNaoPronto(t *testing.T) {
	for _, status := range []string{"recebido", "processando"} {
		t.Run(status, func(t *testing.T) {
			c, pool := subirAPI(t)
			ana := c.usuario("ana@ex.com")
			id := c.enviarVideo(ana, "ferias.mp4")
			if _, err := pool.Exec(t.Context(), `UPDATE videos SET status = $2 WHERE id = $1`, id, status); err != nil {
				t.Fatal(err)
			}

			d := c.baixar(ana, id, nil)

			if d.status != http.StatusConflict {
				t.Fatalf("status = %d, quero 409", d.status)
			}
			if corpo := d.json(t); corpo["status"] != status || corpo["erro"] == "" {
				t.Errorf("corpo = %v, quero erro e status %s", corpo, status)
			}
		})
	}
}

func TestDownload_VideoFalhoDa409ComMotivo(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	encerrar(t, pool, id, "falhou")

	d := c.baixar(ana, id, nil)

	if d.status != http.StatusConflict {
		t.Fatalf("status = %d, quero 409", d.status)
	}
	corpo := d.json(t)
	if corpo["status"] != "falhou" || corpo["motivo"] != "erro_interno" ||
		corpo["motivo_texto"] != "não conseguimos processar, tente novamente" || corpo["erro"] == "" {
		t.Errorf("corpo = %v", corpo)
	}
}

func TestDownload_ZipExpiradoDa410(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	c.concluir(pool, id, time.Now().Add(-time.Minute))

	if d := c.baixar(ana, id, nil); d.status != http.StatusGone {
		t.Errorf("status = %d, quero 410", d.status)
	}
}

// O Vídeo fica para sempre no histórico: sem o link, mas com o prazo que passou
// (ADR-0015).
func TestListagem_ZipExpiradoMostraOPrazoSemDownload(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	expira := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	c.concluir(pool, id, expira)

	lista := c.fazer(http.MethodGet, "/videos", ana, nil)

	itens, _ := lista.corpo["videos"].([]any)
	if len(itens) != 1 {
		t.Fatalf("listagem = %v, quero o Vídeo expirado nela", lista.corpo)
	}
	consulta := c.fazer(http.MethodGet, "/videos/"+id, ana, nil)
	for nome, v := range map[string]map[string]any{"listagem": itens[0].(map[string]any), "consulta": consulta.corpo} {
		if v["status"] != "concluido" || v["zip_expira_em"] != expira.Format(time.RFC3339) {
			t.Errorf("%s: %v, quero concluido com zip_expira_em %s", nome, v, expira.Format(time.RFC3339))
		}
		if link, tem := v["download"]; tem {
			t.Errorf("%s: download = %v, quero nenhum", nome, link)
		}
	}
}

// Zip apagado pelo limpador entre a consulta ao banco e a abertura: é a
// expiração vista do volume.
func TestDownload_ZipSumidoDoVolumeDa410(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	c.concluir(pool, id, time.Now().Add(time.Hour))
	if err := os.Remove(filepath.Join(c.dados, "videos", id, "frames.zip")); err != nil {
		t.Fatal(err)
	}

	if d := c.baixar(ana, id, nil); d.status != http.StatusGone {
		t.Errorf("status = %d, quero 410", d.status)
	}
}

// Dono checado antes de tudo: Vídeo alheio e inexistente dão o mesmo 404, e
// nada do zip vaza.
func TestDownload_ZipDeOutroUsuarioDa404(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	beto := c.usuario("beto@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	c.concluir(pool, id, time.Now().Add(time.Hour))

	for nome, rota := range map[string]string{
		"de outro usuário": id,
		"inexistente":      "0b6f8f3e-1d2c-4c5b-9a8e-7f6d5c4b3a21",
		"id inválido":      "..%2F..%2Fetc",
	} {
		d := c.baixar(beto, rota, nil)
		if d.status != http.StatusNotFound || d.json(t)["erro"] != "vídeo não encontrado" {
			t.Errorf("%s: status %d corpo %q, quero 404 \"vídeo não encontrado\"", nome, d.status, d.corpo)
		}
	}
}

func TestDownload_SemTokenDa401(t *testing.T) {
	c, _ := subirAPI(t)
	if d := c.baixar("", "0b6f8f3e-1d2c-4c5b-9a8e-7f6d5c4b3a21", nil); d.status != http.StatusUnauthorized {
		t.Errorf("status = %d, quero 401", d.status)
	}
}
