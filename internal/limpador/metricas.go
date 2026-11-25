package limpador

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// metricas do limpador (ADR-0015).
type metricas struct {
	itens   *prometheus.CounterVec
	duracao prometheus.Histogram
}

func novasMetricas(registro prometheus.Registerer) *metricas {
	f := promauto.With(registro)
	m := &metricas{
		itens: f.NewCounterVec(prometheus.CounterOpts{
			Name: "fiapx_limpador_itens_total",
			Help: "Itens apagados pelo limpador, por tipo.",
		}, []string{"tipo"}),
		duracao: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "fiapx_limpador_rodada_segundos",
			Help:    "Duração das rodadas do limpador, fora as puladas.",
			Buckets: []float64{.01, .05, .1, .5, 1, 5, 30, 120},
		}),
	}
	m.contar(Resultado{}) // séries desde o início
	return m
}

func (m *metricas) contar(r Resultado) {
	for tipo, n := range map[string]int{
		"zip": r.Zips, "original": r.Originais, "temporario": r.Temporarios, "orfao": r.Orfaos, "outbox": r.Outbox,
	} {
		m.itens.WithLabelValues(tipo).Add(float64(n))
	}
}
