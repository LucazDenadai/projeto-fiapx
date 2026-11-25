package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// timeoutColeta limita a consulta de cada coleta: o Postgres fora não pode
// segurar as outras métricas do processo.
const timeoutColeta = 2 * time.Second

var (
	descPendentes = prometheus.NewDesc("fiapx_outbox_pendentes",
		"Linhas da outbox ainda não publicadas.", nil, nil)
	descIdade = prometheus.NewDesc("fiapx_outbox_idade_segundos",
		"Idade da linha pendente mais antiga da outbox; zero sem pendência.", nil, nil)
)

type coletor struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NovoColetor mede a outbox no Postgres a cada coleta (ADR-0010). O valor é do
// banco, o mesmo em todas as réplicas. Sem resposta do Postgres, as séries
// somem daquela coleta.
func NovoColetor(pool *pgxpool.Pool, log *slog.Logger) prometheus.Collector {
	return coletor{pool: pool, log: log}
}

func (c coletor) Describe(ch chan<- *prometheus.Desc) {
	ch <- descPendentes
	ch <- descIdade
}

func (c coletor) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutColeta)
	defer cancel()
	var pendentes, idade float64
	err := c.pool.QueryRow(ctx, `SELECT count(*)::float8, coalesce(extract(epoch FROM now() - min(criado_em)), 0)::float8
		FROM outbox WHERE publicado_em IS NULL`).Scan(&pendentes, &idade)
	if err != nil {
		c.log.Warn("medir outbox", "erro", err)
		return
	}
	ch <- prometheus.MustNewConstMetric(descPendentes, prometheus.GaugeValue, pendentes)
	ch <- prometheus.MustNewConstMetric(descIdade, prometheus.GaugeValue, idade)
}
