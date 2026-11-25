package pico_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucazDenadai/projeto-fiapx/internal/api"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/limite"
	"github.com/LucazDenadai/projeto-fiapx/internal/pico"
)

func TestMain(m *testing.M) { integracao.Principal(m) }

// subirAPI sobe a API real, com a cota e os limites de tentativas padrão.
func subirAPI(t *testing.T) (url string, pool *pgxpool.Pool) {
	t.Helper()
	pool = integracao.Postgres(t)
	opcoes, prefixo := integracao.Redis(t)
	log := slog.New(slog.DiscardHandler)
	tentativas := limite.Novo(opcoes, limite.Config{Regras: limite.RegrasPadrao(), Prefixo: prefixo}, log)
	t.Cleanup(func() { _ = tentativas.Fechar() })
	h, err := api.Novo(api.Config{
		Pool:       pool,
		SegredoJWT: []byte("segredo-de-teste-com-pelo-menos-32-bytes!"),
		Log:        log,
		Tentativas: tentativas,
		DirDados:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("api.Novo: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, pool
}

// video grava um arquivo com os magic bytes de um MP4: a API não olha além.
func video(t *testing.T) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(caminho, []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2conteudo"), 0o600); err != nil {
		t.Fatal(err)
	}
	return caminho
}

func rodar(t *testing.T, cfg pico.Config) (pico.Resultado, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var saida strings.Builder
	r, err := pico.Rodar(ctx, cfg, &saida)
	if err != nil {
		t.Fatalf("pico: %v\n%s", err, saida.String())
	}
	return r, saida.String()
}

func TestRodar_EnviaTodosDividindoPelaCota(t *testing.T) {
	url, pool := subirAPI(t)
	cfg := pico.Config{URL: url, Video: video(t), Envios: 12}

	r, _ := rodar(t, cfg)

	if r.Aceitos != 12 || r.Recusados != 0 {
		t.Errorf("pico = %+v, quero 12 aceitos", r)
	}
	var total, donos, maximo int
	if err := pool.QueryRow(t.Context(), `SELECT sum(n), count(*), max(n) FROM (SELECT count(*) n FROM videos GROUP BY usuario_id) u`).
		Scan(&total, &donos, &maximo); err != nil {
		t.Fatal(err)
	}
	if total != 12 || donos != 3 || maximo > 5 {
		t.Errorf("%d vídeos de %d usuários, até %d cada; quero 12 de 3, até 5 cada", total, donos, maximo)
	}

	// De novo: reusa os Usuários, e quem está na cota é recusado sem abortar.
	r, _ = rodar(t, cfg)
	if r.Aceitos != 2 || r.Recusados != 10 {
		t.Errorf("segundo pico = %+v, quero só os 2 do último Usuário, que ainda cabem na cota", r)
	}
}

func TestRodar_EsperaTodosTerminarem(t *testing.T) {
	url, pool := subirAPI(t)
	ctx, cancelar := context.WithCancel(t.Context())
	defer cancelar()
	go func() { // o papel dos Workers: termina um Vídeo por vez, um deles com falha
		for n := 0; ctx.Err() == nil; time.Sleep(20 * time.Millisecond) {
			tag, err := pool.Exec(ctx, `UPDATE videos SET status = CASE WHEN $1 = 0 THEN 'falhou' ELSE 'concluido' END,
				motivo = CASE WHEN $1 = 0 THEN 'erro_interno' END
				WHERE id = (SELECT id FROM videos WHERE status = 'recebido' LIMIT 1)`, n)
			if err == nil && tag.RowsAffected() == 1 {
				n++
			}
		}
	}()

	r, saida := rodar(t, pico.Config{URL: url, Video: video(t), Envios: 7, Esperar: true, Intervalo: 10 * time.Millisecond})

	if r.Aceitos != 7 || r.Concluidos != 6 || r.Falhos != 1 {
		t.Errorf("pico = %+v, quero 6 concluídos e 1 falho de 7\n%s", r, saida)
	}
}

func TestRodar_APIForaDaErro(t *testing.T) {
	srv := httptest.NewServer(nil)
	srv.Close()
	if _, err := pico.Rodar(t.Context(), pico.Config{URL: srv.URL, Video: video(t), Envios: 1}, &strings.Builder{}); err == nil {
		t.Error("API fora deveria dar erro")
	}
}
