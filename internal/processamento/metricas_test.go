package processamento_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/LucazDenadai/projeto-fiapx/internal/extracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/processamento"
)

func TestMetricas_ProcessamentosPorResultadoEMotivo(t *testing.T) {
	c := novoCenario(t)
	reg := prometheus.NewRegistry()
	proc := c.processadorCom(t, nil, nil, processamento.Config{Registro: reg}, slog.New(slog.DiscardHandler))
	concluido := c.videoRecebido(t, "recebido", 2*time.Second)
	invalido := c.videoComOriginal(t, "recebido", truncado(t))
	esgotado := c.videoRecebido(t, "processando", time.Second)

	for range 2 { // a entrega duplicada não conta de novo
		proc.Processar(t.Context(), mensagem(concluido, "c1"))
		proc.Processar(t.Context(), mensagem(invalido, "c2"))
		proc.ProcessarDLQ(t.Context(), mensagem(esgotado, "c3"))
	}

	err := testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP fiapx_processamentos_em_andamento Processamentos em andamento nesta réplica.
# TYPE fiapx_processamentos_em_andamento gauge
fiapx_processamentos_em_andamento 0
# HELP fiapx_processamentos_total Processamentos terminados, por resultado e Motivo da falha (vazio em concluido).
# TYPE fiapx_processamentos_total counter
fiapx_processamentos_total{motivo="",resultado="concluido"} 1
fiapx_processamentos_total{motivo="erro_interno",resultado="falhou"} 1
fiapx_processamentos_total{motivo="sem_frames",resultado="falhou"} 0
fiapx_processamentos_total{motivo="tempo_excedido",resultado="falhou"} 0
fiapx_processamentos_total{motivo="video_invalido",resultado="falhou"} 1
fiapx_processamentos_total{motivo="video_muito_longo",resultado="falhou"} 0
`), "fiapx_processamentos_em_andamento", "fiapx_processamentos_total")
	if err != nil {
		t.Error(err)
	}
	// A duração é de quem processou: concluído e inválido, não a DLQ.
	if n, _ := integracao.Metrica(t, reg, "fiapx_processamento_segundos"); n != 2 {
		t.Errorf("durações do Processamento = %v, quero 2", n)
	}
	if n, _ := integracao.Metrica(t, reg, "fiapx_ffmpeg_segundos"); n < 1 {
		t.Errorf("durações do ffmpeg = %v, quero ao menos a do concluído", n)
	}
}

// naExtracao chama fazer no meio do Processamento.
type naExtracao struct {
	processamento.Extrator
	fazer func()
}

func (e naExtracao) ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error) {
	e.fazer()
	return e.Extrator.ExtrairFrames(ctx, arquivo, dir)
}

func TestMetricas_EmAndamentoDuranteOProcessamento(t *testing.T) {
	c := novoCenario(t)
	reg := prometheus.NewRegistry()
	meio := naExtracao{extracao.Novo(extracao.Config{}), func() {
		if v, _ := integracao.Metrica(t, reg, "fiapx_processamentos_em_andamento"); v != 1 {
			t.Errorf("em andamento = %v, quero 1", v)
		}
	}}
	proc := c.processadorCom(t, nil, meio, processamento.Config{Registro: reg}, slog.New(slog.DiscardHandler))

	proc.Processar(t.Context(), mensagem(c.videoRecebido(t, "recebido", time.Second), "c1"))

	if v, _ := integracao.Metrica(t, reg, "fiapx_processamentos_em_andamento"); v != 0 {
		t.Errorf("depois de terminar, em andamento = %v, quero 0", v)
	}
}
