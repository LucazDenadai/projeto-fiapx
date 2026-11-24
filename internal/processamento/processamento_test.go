package processamento_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
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
	if ext == nil {
		ext = extracao.Novo(extracao.Config{})
	}
	return processamento.NovoProcessador(c.pool, arqs, ext, processamento.Config{DirTrabalho: c.trabalho}, slog.New(slog.DiscardHandler))
}

// videoRecebido cria um Usuário e um Vídeo no status dado, com o original de
// duracao no volume, como a API deixa depois do envio.
func (c cenario) videoRecebido(t *testing.T, status string, duracao time.Duration) uuid.UUID {
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
	f, err := os.Open(integracao.Video(t, duracao))
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

// zipQueCai simula um Worker que cai depois de extrair os frames e antes do
// commit: a publicação do zip falha.
type zipQueCai struct{ processamento.Armazenamento }

func (zipQueCai) PublicarZip(context.Context, uuid.UUID, func(io.Writer) error) error {
	return errors.New("worker caiu")
}

func TestProcessar_QuedaAntesDoCommitDevolveEReentregaRefaz(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", 3*time.Second)
	msg := mensagem(id, "c")

	r := c.processador(t, zipQueCai{c.volume}, nil).Processar(t.Context(), msg)
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

	if err := c.processador(t, nil, nil).LimparTrabalho(); err != nil {
		t.Fatalf("limpar: %v", err)
	}
	c.trabalhoVazio(t)
}
