package observabilidade

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// timeoutVerificacao limita cada verificação do /readyz, para o probe não travar
// esperando uma dependência que não responde.
const timeoutVerificacao = 2 * time.Second

// Dependencia é algo sem o qual o processo não consegue trabalhar.
// Verificar devolve erro quando ela não responde.
type Dependencia struct {
	Nome      string
	Verificar func(ctx context.Context) error
}

// HandlerInterno serve a porta interna: /metrics (do registro dado), /healthz
// (processo vivo) e /readyz (todas as dependências respondem). O detalhe da falha vai ao log;
// a resposta cita só o nome da dependência.
func HandlerInterno(log *slog.Logger, metricas prometheus.Gatherer, deps ...Dependencia) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(metricas, promhttp.HandlerOpts{
		// Uma métrica com erro não derruba as outras.
		ErrorHandling: promhttp.ContinueOnError,
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		responder(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		falhas := []string{}
		for _, d := range deps {
			ctx, cancel := context.WithTimeout(r.Context(), timeoutVerificacao)
			err := d.Verificar(ctx)
			cancel()
			if err != nil {
				log.Warn("dependência indisponível", "dependencia", d.Nome, "erro", err)
				falhas = append(falhas, d.Nome)
			}
		}
		if len(falhas) > 0 {
			responder(w, http.StatusServiceUnavailable, map[string]any{"status": "indisponivel", "falhas": falhas})
			return
		}
		responder(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	return mux
}

func responder(w http.ResponseWriter, status int, corpo any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(corpo)
}
