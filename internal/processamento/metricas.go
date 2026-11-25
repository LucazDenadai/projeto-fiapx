package processamento

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/LucazDenadai/projeto-fiapx/internal/motivo"
	"github.com/LucazDenadai/projeto-fiapx/internal/videos"
)

// metricas do Processamento (ADR-0010). Um Processamento conta quando o Vídeo
// termina; a entrega devolvida à fila ou duplicada não é um resultado.
type metricas struct {
	terminados  *prometheus.CounterVec
	duracao     prometheus.Histogram
	ffmpeg      prometheus.Histogram
	emAndamento prometheus.Gauge
}

func novasMetricas(registro prometheus.Registerer) *metricas {
	f := promauto.With(registro)
	// Até 600 s, o timeout padrão do ffmpeg.
	buckets := []float64{.5, 1, 2, 5, 10, 20, 30, 60, 120, 300, 600}
	m := &metricas{
		terminados: f.NewCounterVec(prometheus.CounterOpts{
			Name: "fiapx_processamentos_total",
			Help: "Processamentos terminados, por resultado e Motivo da falha (vazio em concluido).",
		}, []string{"resultado", "motivo"}),
		duracao: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "fiapx_processamento_segundos",
			Help:    "Duração do Processamento que terminou o Vídeo, da entrega ao fim.",
			Buckets: buckets,
		}),
		ffmpeg: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "fiapx_ffmpeg_segundos",
			Help:    "Duração de cada execução do ffmpeg, com sucesso ou não.",
			Buckets: buckets,
		}),
		emAndamento: f.NewGauge(prometheus.GaugeOpts{
			Name: "fiapx_processamentos_em_andamento",
			Help: "Processamentos em andamento nesta réplica.",
		}),
	}
	// Séries desde o início, para a primeira ocorrência aparecer nas taxas.
	m.terminados.WithLabelValues(videos.StatusConcluido, "")
	for _, codigo := range motivo.Todos() {
		m.terminados.WithLabelValues(videos.StatusFalhou, codigo)
	}
	return m
}

func (m *metricas) concluiu() { m.terminados.WithLabelValues(videos.StatusConcluido, "").Inc() }

func (m *metricas) falhou(codigo string) {
	m.terminados.WithLabelValues(videos.StatusFalhou, codigo).Inc()
}
