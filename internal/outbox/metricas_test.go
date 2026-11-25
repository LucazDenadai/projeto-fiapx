package outbox_test

import (
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/outbox"
)

func TestColetor_PendentesEIdadeDaMaisAntiga(t *testing.T) {
	pool := integracao.Postgres(t)
	reg := prometheus.NewRegistry()
	reg.MustRegister(outbox.NovoColetor(pool, slog.New(slog.DiscardHandler)))

	for _, nome := range []string{"fiapx_outbox_pendentes", "fiapx_outbox_idade_segundos"} {
		if v, ok := integracao.Metrica(t, reg, nome); !ok || v != 0 {
			t.Errorf("outbox vazia: %s = %v (%v), quero 0", nome, v, ok)
		}
	}

	if _, err := pool.Exec(t.Context(), `INSERT INTO outbox (exchange, routing_key, payload, criado_em, publicado_em) VALUES
		('', 'x', '{}', now() - interval '2 hours', now()),
		('', 'x', '{}', now() - interval '10 minutes', NULL),
		('', 'x', '{}', now() - interval '1 minute', NULL)`); err != nil {
		t.Fatal(err)
	}

	if v, _ := integracao.Metrica(t, reg, "fiapx_outbox_pendentes"); v != 2 {
		t.Errorf("pendentes = %v, quero 2 (a publicada não conta)", v)
	}
	if v, _ := integracao.Metrica(t, reg, "fiapx_outbox_idade_segundos"); v < 600 || v > 660 {
		t.Errorf("idade = %v s, quero ~600 (a pendente mais antiga)", v)
	}
}

// Postgres fora: só as séries da outbox somem, e a coleta não falha.
func TestColetor_PostgresForaNaoDerrubaAColeta(t *testing.T) {
	pool, err := banco.Abrir(t.Context(), "postgres://x:x@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reg := prometheus.NewRegistry()
	reg.MustRegister(outbox.NovoColetor(pool, slog.New(slog.DiscardHandler)))

	if _, ok := integracao.Metrica(t, reg, "fiapx_outbox_pendentes"); ok {
		t.Error("sem Postgres, a série deveria sumir, não valer zero")
	}
}
