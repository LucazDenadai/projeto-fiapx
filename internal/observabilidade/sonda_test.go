package observabilidade_test

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
)

func TestSondar(t *testing.T) {
	saudavel := httptest.NewServer(observabilidade.HandlerInterno(observabilidade.NovoLogger("t", io.Discard), observabilidade.NovoRegistro(),
		observabilidade.Dependencia{Nome: "postgres", Verificar: ok}))
	defer saudavel.Close()
	doente := httptest.NewServer(observabilidade.HandlerInterno(observabilidade.NovoLogger("t", io.Discard), observabilidade.NovoRegistro(),
		observabilidade.Dependencia{Nome: "postgres", Verificar: quebrada}))
	defer doente.Close()

	if err := observabilidade.Sondar(context.Background(), saudavel.URL+"/readyz"); err != nil {
		t.Errorf("processo pronto deveria passar na sonda: %v", err)
	}
	if err := observabilidade.Sondar(context.Background(), doente.URL+"/readyz"); err == nil {
		t.Error("processo com dependência fora deveria falhar na sonda")
	}
	if err := observabilidade.Sondar(context.Background(), "http://127.0.0.1:1/readyz"); err == nil {
		t.Error("processo fora do ar deveria falhar na sonda")
	}
}
