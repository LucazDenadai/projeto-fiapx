package integracao

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrica devolve a soma das séries de uma métrica: o valor do contador ou
// gauge, ou o número de observações do histograma. Métrica ausente dá zero e
// false.
func Metrica(t *testing.T, reg prometheus.Gatherer, nome string) (float64, bool) {
	t.Helper()
	familias, err := reg.Gather()
	if err != nil {
		t.Fatalf("coletar métricas: %v", err)
	}
	for _, f := range familias {
		if f.GetName() != nome {
			continue
		}
		soma := 0.0
		for _, m := range f.GetMetric() {
			soma += m.GetCounter().GetValue() + m.GetGauge().GetValue() + float64(m.GetHistogram().GetSampleCount())
		}
		return soma, true
	}
	return 0, false
}
