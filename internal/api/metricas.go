package api

import (
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Causas do 429, para o rótulo da métrica.
const (
	causaCota       = "cota"
	causaTentativas = "tentativas"
)

// rotaDesconhecida junta tudo o que não é rota da API: o caminho vem do
// cliente e não pode virar rótulo.
const rotaDesconhecida = "desconhecida"

type metricas struct {
	requisicoes *prometheus.HistogramVec
	aceitos     prometheus.Counter
	limitadas   *prometheus.CounterVec
}

func novasMetricas(registro prometheus.Registerer) *metricas {
	f := promauto.With(registro)
	m := &metricas{
		requisicoes: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "fiapx_http_requisicao_segundos",
			Help: "Latência das requisições à API, por rota e status.",
			// Até o envio de um Vídeo grande (timeout de 10 min).
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 180, 600},
		}, []string{"rota", "status"}),
		aceitos: f.NewCounter(prometheus.CounterOpts{
			Name: "fiapx_videos_aceitos_total",
			Help: "Vídeos aceitos pela API (202).",
		}),
		limitadas: f.NewCounterVec(prometheus.CounterOpts{
			Name: "fiapx_respostas_429_total",
			Help: "Respostas 429, por causa: cota de Vídeos ou limite de tentativas.",
		}, []string{"causa"}),
	}
	m.limitadas.WithLabelValues(causaCota)
	m.limitadas.WithLabelValues(causaTentativas)
	return m
}

// medir observa a latência de cada requisição. O rótulo é o padrão da rota
// (GET /videos/{id}), nunca o caminho pedido (ADR-0010): as rotas protegidas
// ficam num roteador interno, então o padrão delas é consultado nele, e vale
// também para a requisição que o token barrou.
func (m *metricas) medir(proximo, protegidas *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		resp := &respostaMedida{ResponseWriter: w, status: http.StatusOK}
		// Em defer: o download abortado pelo cliente também conta.
		defer func() {
			m.requisicoes.WithLabelValues(rota(proximo, protegidas, r), strconv.Itoa(resp.status)).
				Observe(time.Since(inicio).Seconds())
		}()
		proximo.ServeHTTP(resp, r)
	})
}

func rota(publicas, protegidas *http.ServeMux, r *http.Request) string {
	for _, mux := range []*http.ServeMux{protegidas, publicas} {
		// Os prefixos que levam às protegidas não são rotas de negócio.
		if _, padrao := mux.Handler(r); padrao != "" && padrao != prefixoVideos && padrao != prefixoVideos+"/" {
			return padrao
		}
	}
	return rotaDesconhecida
}

// respostaMedida guarda o status da resposta.
type respostaMedida struct {
	http.ResponseWriter
	status int
	enviou bool
}

func (r *respostaMedida) WriteHeader(status int) {
	if !r.enviou {
		r.status, r.enviou = status, true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *respostaMedida) Write(b []byte) (int, error) {
	r.enviou = true
	return r.ResponseWriter.Write(b)
}

// ReadFrom mantém o sendfile do download, que o embrulho esconderia.
func (r *respostaMedida) ReadFrom(leitor io.Reader) (int64, error) {
	r.enviou = true
	return io.Copy(r.ResponseWriter, leitor)
}

// Unwrap deixa o http.ResponseController chegar ao ResponseWriter de verdade
// (o prazo de leitura do envio depende disso).
func (r *respostaMedida) Unwrap() http.ResponseWriter { return r.ResponseWriter }
