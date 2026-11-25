package observabilidade_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
)

func ok(context.Context) error { return nil }

func quebrada(context.Context) error {
	return errors.New("dial tcp 10.0.0.5:5432: connection refused")
}

func get(t *testing.T, h http.Handler, caminho string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, caminho, nil))
	return rec.Code, rec.Body.String()
}

func TestPortaInterna_TudoRespondendo(t *testing.T) {
	h := observabilidade.HandlerInterno(observabilidade.NovoLogger("api", io.Discard), observabilidade.NovoRegistro(),
		observabilidade.Dependencia{Nome: "postgres", Verificar: ok},
		observabilidade.Dependencia{Nome: "rabbitmq", Verificar: ok},
	)

	if code, _ := get(t, h, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d, quero 200", code)
	}
	if code, _ := get(t, h, "/readyz"); code != http.StatusOK {
		t.Errorf("/readyz = %d, quero 200", code)
	}
}

func TestPortaInterna_DependenciaFora(t *testing.T) {
	var log strings.Builder
	h := observabilidade.HandlerInterno(observabilidade.NovoLogger("api", &log), observabilidade.NovoRegistro(),
		observabilidade.Dependencia{Nome: "postgres", Verificar: quebrada},
		observabilidade.Dependencia{Nome: "rabbitmq", Verificar: ok},
	)

	code, corpo := get(t, h, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d, quero 503", code)
	}
	if !strings.Contains(corpo, "postgres") || strings.Contains(corpo, "rabbitmq") {
		t.Errorf("corpo deve citar só a dependência fora: %s", corpo)
	}
	if strings.Contains(corpo, "10.0.0.5") {
		t.Errorf("detalhe do erro vazou na resposta: %s", corpo)
	}
	if !strings.Contains(log.String(), "connection refused") {
		t.Errorf("detalhe do erro deveria ir ao log: %s", log.String())
	}

	if code, _ := get(t, h, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d com dependência fora, quero 200", code)
	}
}

func TestPortaInterna_Metricas(t *testing.T) {
	registro := observabilidade.NovoRegistro()
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "fiapx_teste_total", Help: "teste"})
	registro.MustRegister(c)
	c.Inc()
	h := observabilidade.HandlerInterno(observabilidade.NovoLogger("api", io.Discard), registro)

	code, corpo := get(t, h, "/metrics")
	if code != http.StatusOK || !strings.Contains(corpo, "fiapx_teste_total 1") || !strings.Contains(corpo, "go_goroutines") {
		t.Errorf("/metrics = %d, quero 200 com as métricas do processo e do registro:\n%.400s", code, corpo)
	}
}
