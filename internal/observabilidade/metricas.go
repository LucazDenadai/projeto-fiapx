package observabilidade

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NovoRegistro devolve o registro de métricas de um processo, já com as do
// runtime do Go e do próprio processo (ADR-0010).
func NovoRegistro() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return r
}
