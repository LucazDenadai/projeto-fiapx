package processamento_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/extracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/processamento"
)

func TestMain(m *testing.M) { integracao.Principal(m) }

// cenario é um Worker de teste: banco, volume e diretório de trabalho próprios.
type cenario struct {
	pool     *pgxpool.Pool
	volume   *armazenamento.Volume
	trabalho string
}

func novoCenario(t *testing.T) cenario {
	t.Helper()
	return cenario{
		pool:     integracao.Postgres(t),
		volume:   armazenamento.NovoVolume(t.TempDir()),
		trabalho: t.TempDir(),
	}
}

func (c cenario) processador(t *testing.T, arqs processamento.Armazenamento, ext processamento.Extrator) *processamento.Processador {
	t.Helper()
	if arqs == nil {
		arqs = c.volume
	}
	return c.processadorCom(t, arqs, ext, processamento.Config{}, slog.New(slog.DiscardHandler))
}

// processadorCom monta o Processador com a configuração dada, sempre no
// diretório de trabalho do cenário.
func (c cenario) processadorCom(t *testing.T, arqs processamento.Armazenamento, ext processamento.Extrator, cfg processamento.Config, log *slog.Logger) *processamento.Processador {
	t.Helper()
	if arqs == nil {
		arqs = c.volume
	}
	if ext == nil {
		ext = extracao.Novo(extracao.Config{})
	}
	cfg.DirTrabalho = c.trabalho
	return processamento.NovoProcessador(c.pool, arqs, ext, cfg, log)
}

// videoRecebido cria um Usuário e um Vídeo no status dado, com o original de
// duracao no volume, como a API deixa depois do envio.
func (c cenario) videoRecebido(t *testing.T, status string, duracao time.Duration) uuid.UUID {
	t.Helper()
	return c.videoComOriginal(t, status, integracao.Video(t, duracao))
}

// videoComOriginal é o videoRecebido com o arquivo dado como original.
func (c cenario) videoComOriginal(t *testing.T, status, original string) uuid.UUID {
	t.Helper()
	ctx := t.Context()
	var usuario uuid.UUID
	if err := c.pool.QueryRow(ctx, `INSERT INTO usuarios (email, senha_hash) VALUES ($1, 'x') RETURNING id`,
		uuid.NewString()+"@x.com").Scan(&usuario); err != nil {
		t.Fatalf("inserir usuário: %v", err)
	}
	id := uuid.New()
	if _, err := c.pool.Exec(ctx, `INSERT INTO videos (id, usuario_id, nome_original, status) VALUES ($1, $2, 'ferias.mp4', $3)`,
		id, usuario, status); err != nil {
		t.Fatalf("inserir vídeo: %v", err)
	}
	f, err := os.Open(original) //nolint:gosec // arquivo gerado pelo teste
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := c.volume.GravarOriginal(ctx, id, f); err != nil {
		t.Fatalf("gravar original: %v", err)
	}
	return id
}

func mensagem(id uuid.UUID, correlationID string) []byte {
	b, _ := json.Marshal(map[string]string{"video_id": id.String(), "correlation_id": correlationID})
	return b
}

func (c cenario) status(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := c.pool.QueryRow(t.Context(), `SELECT status FROM videos WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("ler status: %v", err)
	}
	return s
}

type evento struct {
	exchange, routingKey string
	payload              map[string]any
}

func (c cenario) eventos(t *testing.T) []evento {
	t.Helper()
	rows, err := c.pool.Query(t.Context(), `SELECT exchange, routing_key, payload FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatalf("ler outbox: %v", err)
	}
	defer rows.Close()
	var lista []evento
	for rows.Next() {
		var e evento
		var bruto []byte
		if err := rows.Scan(&e.exchange, &e.routingKey, &bruto); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(bruto, &e.payload); err != nil {
			t.Fatal(err)
		}
		lista = append(lista, e)
	}
	return lista
}

func (c cenario) trabalhoVazio(t *testing.T) {
	t.Helper()
	ents, err := os.ReadDir(c.trabalho)
	if err != nil {
		t.Fatalf("ler diretório de trabalho: %v", err)
	}
	if len(ents) != 0 {
		t.Errorf("diretório de trabalho tem %d entradas, quero vazio", len(ents))
	}
}

func entradasDoZip(t *testing.T, caminho string) []string {
	t.Helper()
	z, err := zip.OpenReader(caminho)
	if err != nil {
		t.Fatalf("abrir zip: %v", err)
	}
	defer func() { _ = z.Close() }()
	var nomes []string
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		cab := make([]byte, 4)
		_, _ = io.ReadFull(r, cab)
		_ = r.Close()
		if string(cab[1:4]) != "PNG" {
			t.Errorf("%s no zip não é PNG", f.Name)
		}
		nomes = append(nomes, f.Name)
	}
	return nomes
}

// espiao registra o status do Vídeo no banco enquanto o ffmpeg roda.
type espiao struct {
	processamento.Extrator
	c                cenario
	id               uuid.UUID
	statusNaExtracao string
	chamadas         int
}

func (e *espiao) ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error) {
	e.chamadas++
	_ = e.c.pool.QueryRow(ctx, `SELECT status FROM videos WHERE id = $1`, e.id).Scan(&e.statusNaExtracao)
	return e.Extrator.ExtrairFrames(ctx, arquivo, dir)
}

func TestProcessar_VideoConcluido(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", 5*time.Second)
	esp := &espiao{Extrator: extracao.Novo(extracao.Config{}), c: c, id: id}

	r := c.processador(t, nil, esp).Processar(t.Context(), mensagem(id, "corr-1"))

	if r != mensageria.Confirmar {
		t.Fatalf("resultado = %v, quero Confirmar", r)
	}
	if esp.statusNaExtracao != "processando" {
		t.Errorf("status durante a extração = %q, quero processando", esp.statusNaExtracao)
	}
	if s := c.status(t, id); s != "concluido" {
		t.Errorf("status = %s, quero concluido", s)
	}
	zipPath := filepath.Join(filepath.Dir(c.volume.Original(id)), "frames.zip")
	if nomes := entradasDoZip(t, zipPath); len(nomes) != 5 {
		t.Errorf("frames no zip = %v, quero 5", nomes)
	}
	if _, err := os.Stat(c.volume.Original(id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("original ainda existe: %v", err)
	}
	evs := c.eventos(t)
	if len(evs) != 1 {
		t.Fatalf("eventos na outbox = %d, quero 1", len(evs))
	}
	e := evs[0]
	if e.exchange != "processamento.eventos" || e.routingKey != "concluido" ||
		e.payload["video_id"] != id.String() || e.payload["correlation_id"] != "corr-1" || e.payload["ocorrido_em"] == nil {
		t.Errorf("evento = %+v", e)
	}
	c.trabalhoVazio(t)
}

func TestProcessar_ZipExpiraNaRetencao(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", time.Second)

	c.processador(t, nil, nil).Processar(t.Context(), mensagem(id, "c"))

	var expira, atualizado time.Time
	if err := c.pool.QueryRow(t.Context(), `SELECT zip_expira_em, atualizado_em FROM videos WHERE id = $1`, id).
		Scan(&expira, &atualizado); err != nil {
		t.Fatalf("ler zip_expira_em: %v", err)
	}
	if d := expira.Sub(atualizado); d != processamento.RetencaoZipPadrao {
		t.Errorf("zip_expira_em - atualizado_em = %v, quero %v", d, processamento.RetencaoZipPadrao)
	}
}

// zipQueCai simula um Worker que cai depois de extrair os frames e publicar o
// zip, antes do commit do concluido.
type zipQueCai struct{ processamento.Armazenamento }

func (z zipQueCai) PublicarZip(ctx context.Context, id uuid.UUID, escrever func(io.Writer) error) error {
	if err := z.Armazenamento.PublicarZip(ctx, id, escrever); err != nil {
		return err
	}
	return errors.New("worker caiu")
}

func TestProcessar_QuedaAntesDoCommitDevolveEReentregaRefaz(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", 3*time.Second)
	msg := mensagem(id, "c")

	// Orçamento curto: a queda se repete em toda nova tentativa.
	semRetry := processamento.Config{OrcamentoRetry: time.Millisecond}
	r := c.processadorCom(t, zipQueCai{c.volume}, nil, semRetry, slog.New(slog.DiscardHandler)).Processar(t.Context(), msg)
	if r != mensageria.Devolver {
		t.Fatalf("resultado com queda = %v, quero Devolver (sem ack)", r)
	}
	if s := c.status(t, id); s != "processando" {
		t.Errorf("status depois da queda = %s, quero processando", s)
	}
	if len(c.eventos(t)) != 0 {
		t.Error("evento gravado sem commit do concluido")
	}
	c.trabalhoVazio(t)

	// Outro Worker recebe a mesma mensagem e refaz do zero.
	if r := c.processador(t, nil, nil).Processar(t.Context(), msg); r != mensageria.Confirmar {
		t.Fatalf("resultado da reentrega = %v, quero Confirmar", r)
	}
	if s := c.status(t, id); s != "concluido" {
		t.Errorf("status = %s, quero concluido", s)
	}
	zipPath := filepath.Join(filepath.Dir(c.volume.Original(id)), "frames.zip")
	if nomes := entradasDoZip(t, zipPath); len(nomes) != 3 {
		t.Errorf("frames no zip = %d, quero 3", len(nomes))
	}
}

func TestProcessar_VideoTerminalConfirmaSemReprocessar(t *testing.T) {
	for _, status := range []string{"concluido", "falhou"} {
		t.Run(status, func(t *testing.T) {
			c := novoCenario(t)
			id := c.videoRecebido(t, "recebido", time.Second)
			motivo := any(nil)
			if status == "falhou" {
				motivo = "erro_interno"
			}
			if _, err := c.pool.Exec(t.Context(), `UPDATE videos SET status = $2, motivo = $3 WHERE id = $1`, id, status, motivo); err != nil {
				t.Fatal(err)
			}
			esp := &espiao{Extrator: extracao.Novo(extracao.Config{}), c: c, id: id}

			r := c.processador(t, nil, esp).Processar(t.Context(), mensagem(id, "c"))

			if r != mensageria.Confirmar {
				t.Errorf("resultado = %v, quero Confirmar", r)
			}
			if esp.chamadas != 0 {
				t.Error("reprocessou um Vídeo terminal")
			}
			if s := c.status(t, id); s != status {
				t.Errorf("status = %s, quero %s", s, status)
			}
			if len(c.eventos(t)) != 0 {
				t.Error("gravou evento para Vídeo terminal")
			}
		})
	}
}

func TestProcessar_MensagemSemVideoEhDescartada(t *testing.T) {
	c := novoCenario(t)
	for nome, corpo := range map[string][]byte{
		"video inexistente": mensagem(uuid.New(), "c"),
		"json invalido":     []byte("{"),
		"id invalido":       []byte(`{"video_id": "../../etc"}`),
	} {
		t.Run(nome, func(t *testing.T) {
			if r := c.processador(t, nil, nil).Processar(t.Context(), corpo); r != mensageria.Confirmar {
				t.Errorf("resultado = %v, quero Confirmar (descartar)", r)
			}
		})
	}
}

// extratorQueFalha extrai os frames e depois falha, deixando sobra no temporário.
type extratorQueFalha struct{ processamento.Extrator }

func (e extratorQueFalha) ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error) {
	_, _ = e.Extrator.ExtrairFrames(ctx, arquivo, dir)
	return nil, errors.New("ffmpeg saiu com erro")
}

func TestProcessar_TemporarioLimpoNoErro(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", 2*time.Second)

	r := c.processador(t, nil, extratorQueFalha{extracao.Novo(extracao.Config{})}).Processar(t.Context(), mensagem(id, "c"))

	if r != mensageria.Devolver {
		t.Errorf("resultado = %v, quero Devolver", r)
	}
	c.trabalhoVazio(t)
}

func TestLimparTrabalho_ApagaSobrasDeQueda(t *testing.T) {
	c := novoCenario(t)
	sobra := filepath.Join(c.trabalho, "video-123", "frame_00001.png")
	if err := os.MkdirAll(filepath.Dir(sobra), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sobra, []byte(strings.Repeat("x", 10)), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.processador(t, nil, nil).LimparTrabalho(t.Context()); err != nil {
		t.Fatalf("limpar: %v", err)
	}
	c.trabalhoVazio(t)
}

// lento gasta parte do prazo em cada execução; somadas, passam do prazo.
type lento struct{ processamento.Extrator }

func esperarOuPrazo(ctx context.Context) error {
	select {
	case <-time.After(300 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l lento) Sondar(ctx context.Context, arquivo string) (extracao.Sonda, error) {
	if err := esperarOuPrazo(ctx); err != nil {
		return extracao.Sonda{}, err
	}
	return l.Extrator.Sondar(ctx, arquivo)
}

func (l lento) ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error) {
	if err := esperarOuPrazo(ctx); err != nil {
		return nil, err
	}
	return l.Extrator.ExtrairFrames(ctx, arquivo, dir)
}

// truncado é um MP4 com os magic bytes certos, mas cortado antes do índice
// (moov): passa na API e o ffprobe não consegue ler.
func truncado(t *testing.T) string {
	t.Helper()
	inteiro, err := os.ReadFile(integracao.Video(t, 3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	caminho := filepath.Join(t.TempDir(), "truncado.mp4")
	if err := os.WriteFile(caminho, inteiro[:2000], 0o600); err != nil { //nolint:gosec // caminho do próprio teste
		t.Fatal(err)
	}
	return caminho
}

func TestProcessar_FalhaPermanente(t *testing.T) {
	casos := []struct {
		nome     string
		original func(t *testing.T) string
		ext      processamento.Extrator
		cfg      processamento.Config
		motivo   string
	}{
		{
			nome:     "corrompido",
			original: truncado,
			motivo:   "video_invalido",
		},
		{
			nome:     "acima da duração máxima",
			original: func(t *testing.T) string { return integracao.Video(t, 3*time.Second) },
			cfg:      processamento.Config{DuracaoMaxima: 2 * time.Second},
			motivo:   "video_muito_longo",
		},
		{
			// Sem duração no contêiner, a contagem de frames (1 por segundo)
			// faz a conferência.
			nome:     "acima da duração máxima, sem duração no contêiner",
			original: func(t *testing.T) string { return integracao.VideoSemDuracao(t, 5*time.Second) },
			cfg:      processamento.Config{DuracaoMaxima: 2 * time.Second},
			motivo:   "video_muito_longo",
		},
		{
			// Com 1 frame por segundo, menos de meio segundo não dá frame
			// nenhum, e o ffmpeg sai sem erro.
			nome:     "sem frames",
			original: func(t *testing.T) string { return integracao.Video(t, 400*time.Millisecond) },
			motivo:   "sem_frames",
		},
		{
			// Cada execução cabe em 500 ms; as duas juntas, não: o prazo vale
			// para o Processamento.
			nome:     "timeout",
			original: func(t *testing.T) string { return integracao.Video(t, time.Second) },
			ext:      lento{extracao.Novo(extracao.Config{})},
			cfg:      processamento.Config{TimeoutExtracao: 500 * time.Millisecond},
			motivo:   "tempo_excedido",
		},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			c := novoCenario(t)
			id := c.videoComOriginal(t, "recebido", tc.original(t))
			proc := c.processadorCom(t, nil, tc.ext, tc.cfg, slog.New(slog.DiscardHandler))

			if r := proc.Processar(t.Context(), mensagem(id, "corr-9")); r != mensageria.Confirmar {
				t.Fatalf("resultado = %v, quero Confirmar (falha permanente não volta à fila)", r)
			}
			var status, motivo, email string
			if err := c.pool.QueryRow(t.Context(), `SELECT v.status, v.motivo, u.email FROM videos v
				JOIN usuarios u ON u.id = v.usuario_id WHERE v.id = $1`, id).Scan(&status, &motivo, &email); err != nil {
				t.Fatalf("ler vídeo: %v", err)
			}
			if status != "falhou" || motivo != tc.motivo {
				t.Errorf("status = %s, motivo = %s; quero falhou, %s", status, motivo, tc.motivo)
			}
			evs := c.eventos(t)
			if len(evs) != 1 {
				t.Fatalf("eventos na outbox = %d, quero 1", len(evs))
			}
			e := evs[0]
			if e.exchange != "processamento.eventos" || e.routingKey != "falhou" {
				t.Errorf("evento em %s/%s, quero processamento.eventos/falhou", e.exchange, e.routingKey)
			}
			quero := map[string]any{"video_id": id.String(), "correlation_id": "corr-9", "email": email,
				"nome_original": "ferias.mp4", "motivo": tc.motivo}
			for campo, valor := range quero {
				if e.payload[campo] != valor {
					t.Errorf("evento[%s] = %v, quero %v", campo, e.payload[campo], valor)
				}
			}
			if e.payload["ocorrido_em"] == nil || len(e.payload) != len(quero)+1 {
				t.Errorf("evento = %v, quero só os campos do ADR-0008", e.payload)
			}
			if _, err := os.Stat(c.volume.Original(id)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("original ainda existe: %v", err)
			}
			c.trabalhoVazio(t)
		})
	}
}

func TestProcessar_SemDuracaoNoContainerConclui(t *testing.T) {
	c := novoCenario(t)
	id := c.videoComOriginal(t, "recebido", integracao.VideoSemDuracao(t, 3*time.Second))

	if r := c.processador(t, nil, nil).Processar(t.Context(), mensagem(id, "c")); r != mensageria.Confirmar {
		t.Fatalf("resultado = %v, quero Confirmar", r)
	}
	if s := c.status(t, id); s != "concluido" {
		t.Errorf("status = %s, quero concluido", s)
	}
}

// A saída do ffmpeg só vai ao log, junto do correlation_id (ADR-0004).
func TestProcessar_SaidaDoFFmpegSoNoLog(t *testing.T) {
	c := novoCenario(t)
	id := c.videoComOriginal(t, "recebido", truncado(t))
	var buf strings.Builder
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	c.processadorCom(t, nil, nil, processamento.Config{}, log).Processar(t.Context(), mensagem(id, "corr-log"))

	var achou bool
	for linha := range strings.Lines(buf.String()) {
		if strings.Contains(linha, "moov atom not found") && strings.Contains(linha, `"correlation_id":"corr-log"`) {
			achou = true
		}
	}
	if !achou {
		t.Errorf("log sem a saída do ffprobe com o correlation_id:\n%s", buf.String())
	}
	if p := c.eventos(t)[0].payload; strings.Contains(fmt.Sprint(p), "moov") {
		t.Errorf("evento vaza a saída do ffprobe: %v", p)
	}
}

// Vídeo que ficou terminal por outra entrega no meio do caminho: a falha não
// sobrescreve o status nem gera evento.
func TestProcessar_FalhaDeVideoJaTerminalNaoGravaEvento(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", 400*time.Millisecond)
	ext := concluiNoMeio{Extrator: extracao.Novo(extracao.Config{}), c: c, id: id}

	if r := c.processador(t, nil, ext).Processar(t.Context(), mensagem(id, "c")); r != mensageria.Confirmar {
		t.Fatalf("resultado = %v, quero Confirmar", r)
	}
	if s := c.status(t, id); s != "concluido" {
		t.Errorf("status = %s, quero concluido (terminal é final)", s)
	}
	if n := len(c.eventos(t)); n != 0 {
		t.Errorf("eventos na outbox = %d, quero 0", n)
	}
}

// concluiNoMeio simula outra entrega que conclui o Vídeo enquanto este
// Processamento ainda extrai.
type concluiNoMeio struct {
	processamento.Extrator
	c  cenario
	id uuid.UUID
}

func (e concluiNoMeio) ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error) {
	if _, err := e.c.pool.Exec(ctx, `UPDATE videos SET status = 'concluido' WHERE id = $1`, e.id); err != nil {
		return nil, err
	}
	return e.Extrator.ExtrairFrames(ctx, arquivo, dir)
}

// O Worker desligando no meio da extração não é timeout: a mensagem volta à
// fila para outro Worker.
func TestProcessar_DesligamentoDevolveSemFalhar(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", time.Second)
	ctx, cancelar := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancelar)

	r := c.processador(t, nil, lento{extracao.Novo(extracao.Config{})}).Processar(ctx, mensagem(id, "c"))

	if r != mensageria.Devolver {
		t.Errorf("resultado = %v, quero Devolver", r)
	}
	if s := c.status(t, id); s != "processando" {
		t.Errorf("status = %s, quero processando", s)
	}
	if _, err := os.Stat(c.volume.Original(id)); err != nil {
		t.Errorf("original sumiu: %v", err)
	}
}

// Original sumido do volume é problema de infraestrutura, não do vídeo: não
// vira video_invalido.
func TestProcessar_OriginalAusenteDevolve(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", time.Second)
	if err := c.volume.ApagarOriginal(t.Context(), id); err != nil {
		t.Fatal(err)
	}

	if r := c.processador(t, nil, nil).Processar(t.Context(), mensagem(id, "c")); r != mensageria.Devolver {
		t.Errorf("resultado = %v, quero Devolver", r)
	}
	if s := c.status(t, id); s != "processando" {
		t.Errorf("status = %s, quero processando", s)
	}
}
