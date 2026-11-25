package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/LucazDenadai/projeto-fiapx/internal/api"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/limite"
)

// rotulos devolve as combinações dos rótulos dados nas séries de um histograma,
// com as observações de cada uma.
func rotulos(t *testing.T, reg *prometheus.Registry, metrica string, nomes ...string) map[string]float64 {
	t.Helper()
	familias, err := reg.Gather()
	if err != nil {
		t.Fatalf("coletar: %v", err)
	}
	total := map[string]float64{}
	for _, f := range familias {
		if f.GetName() != metrica {
			continue
		}
		for _, m := range f.GetMetric() {
			var valores []string
			for _, l := range m.GetLabel() {
				if slices.Contains(nomes, l.GetName()) {
					valores = append(valores, l.GetValue())
				}
			}
			total[strings.Join(valores, " ")] += float64(m.GetHistogram().GetSampleCount())
		}
	}
	return total
}

func TestMetricas_NaoSaemNaPortaPublica(t *testing.T) {
	c, _ := subirAPI(t)
	if r := c.fazer(http.MethodGet, "/metrics", "", nil); r.status != http.StatusNotFound {
		t.Errorf("/metrics na porta pública = %d, quero 404", r.status)
	}
}

func TestMetricas_RequisicoesPorPadraoDeRota(t *testing.T) {
	reg := prometheus.NewRegistry()
	c, _ := subirAPI(t, func(cfg *api.Config) { cfg.Registro = reg })
	ana := c.usuario("ana@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	c.fazer(http.MethodGet, "/videos/"+id, ana, nil)
	c.fazer(http.MethodGet, "/videos/"+id, "", nil) // sem token: 401
	c.fazer(http.MethodGet, "/videos/"+id+"/zip", ana, nil)
	c.fazer(http.MethodGet, "/videos/"+id+"/nada", ana, nil)
	c.fazer(http.MethodGet, "/ana@ex.com", "", nil)
	c.fazer("INVENTADO", "/videos", ana, nil)

	quero := map[string]float64{
		"POST /usuarios 201":       1,
		"POST /login 200":          1,
		"POST /videos 202":         1,
		"GET /videos/{id} 200":     1,
		"GET /videos/{id} 401":     1,
		"GET /videos/{id}/zip 409": 1,
		"desconhecida 404":         2,
		"desconhecida 405":         1,
	}
	got := rotulos(t, reg, "fiapx_http_requisicao_segundos", "rota", "status")
	for k, n := range quero {
		if got[k] != n {
			t.Errorf("%s = %v, quero %v", k, got[k], n)
		}
	}
	if len(got) != len(quero) {
		t.Errorf("séries = %v, quero só %v", got, quero)
	}
}

func TestMetricas_AceitosE429PorCausa(t *testing.T) {
	reg := prometheus.NewRegistry()
	c, _ := subirAPI(t, func(cfg *api.Config) { cfg.Registro, cfg.CotaVideos = reg, 1 })
	ana := c.usuario("ana@ex.com")
	c.enviarVideo(ana, "a.mp4")
	if r := c.enviar(ana, arquivo{"video", "b.mp4", mp4(4096)}); r.status != http.StatusTooManyRequests {
		t.Fatalf("segundo envio = %d, quero 429", r.status)
	}
	for range 6 {
		c.entrar("beto@ex.com", "errada")
	}

	err := testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP fiapx_respostas_429_total Respostas 429, por causa: cota de Vídeos ou limite de tentativas.
# TYPE fiapx_respostas_429_total counter
fiapx_respostas_429_total{causa="cota"} 1
fiapx_respostas_429_total{causa="tentativas"} 1
# HELP fiapx_videos_aceitos_total Vídeos aceitos pela API (202).
# TYPE fiapx_videos_aceitos_total counter
fiapx_videos_aceitos_total 1
`), "fiapx_respostas_429_total", "fiapx_videos_aceitos_total")
	if err != nil {
		t.Error(err)
	}
}

func TestMetricas_FalhaDoRedis(t *testing.T) {
	reg := prometheus.NewRegistry()
	h, err := api.Novo(api.Config{
		Pool:       integracao.Postgres(t),
		SegredoJWT: []byte(segredoTeste),
		Log:        slog.New(slog.DiscardHandler),
		Tentativas: novasTentativas(t, &redis.Options{Addr: "127.0.0.1:1"}, limite.Config{Regras: limite.RegrasPadrao(), Registro: reg}),
		DirDados:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cliente{t: t, srv: srv}.cadastrar("ana@ex.com", "Forte#123")

	if n, _ := integracao.Metrica(t, reg, "fiapx_redis_falhas_total"); n != 1 {
		t.Errorf("fiapx_redis_falhas_total = %v, quero 1", n)
	}
}

// O medidor não pode esconder do servidor que o corpo passou do limite: ele
// fecha a conexão em vez de ler o resto do envio.
func TestMetricas_CorpoGrandeDemaisFechaAConexao(t *testing.T) {
	c, _ := subirAPI(t)
	corpo := io.MultiReader(strings.NewReader(`{"email":"`), strings.NewReader(strings.Repeat("a", 8<<10))) // sem Content-Length
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, c.srv.URL+"/login", corpo)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest || !res.Close {
		t.Errorf("status %d, Connection: close = %v; quero 400 com a conexão fechada", res.StatusCode, res.Close)
	}
}
