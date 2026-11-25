package notificacao_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/notificacao"
	"github.com/LucazDenadai/projeto-fiapx/internal/retentativa"
)

// smtpInstavel falha nas primeiras tentativas.
type smtpInstavel struct{ falhas int }

func (s *smtpInstavel) Enviar(context.Context, string, string, []byte) error {
	if s.falhas > 0 {
		s.falhas--
		return errors.New("451 tente mais tarde")
	}
	return nil
}

func TestMetricas_EnviadasEFalhasDoSMTP(t *testing.T) {
	reg := prometheus.NewRegistry()
	n := notificacao.NovoNotificador(&smtpInstavel{falhas: 2}, notificacao.Config{
		Remetente: "nao-responda@fiapx.local",
		Retry:     retentativa.Politica{Inicial: time.Millisecond, Orcamento: time.Minute},
		Registro:  reg,
	}, slog.New(slog.DiscardHandler))
	corpo, err := json.Marshal(eventoFalhou("ana@ex.com"))
	if err != nil {
		t.Fatal(err)
	}

	if r := n.Notificar(t.Context(), corpo); r != mensageria.Confirmar {
		t.Fatalf("resultado = %v, quero Confirmar", r)
	}
	n.Notificar(t.Context(), []byte("ilegível")) // não chega ao SMTP

	for nome, quero := range map[string]float64{"fiapx_notificacoes_enviadas_total": 1, "fiapx_smtp_falhas_total": 2} {
		if v, _ := integracao.Metrica(t, reg, nome); v != quero {
			t.Errorf("%s = %v, quero %v", nome, v, quero)
		}
	}
}
