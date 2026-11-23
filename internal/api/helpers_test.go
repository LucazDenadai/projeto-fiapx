package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/LucazDenadai/projeto-fiapx/internal/api"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/limite"
)

func TestMain(m *testing.M) { integracao.Principal(m) }

const segredoTeste = "segredo-de-teste-com-pelo-menos-32-bytes!"

type cliente struct {
	t   *testing.T
	srv *httptest.Server
	// cabecalhos vão em toda requisição deste cliente.
	cabecalhos http.Header
}

type resposta struct {
	status int
	corpo  map[string]any
	header http.Header
}

// fazerComEsquema manda a requisição com "Authorization: <esquema> <token>";
// token vazio omite o header.
func (c cliente) fazerComEsquema(metodo, rota, esquema, token string, corpo any) resposta {
	c.t.Helper()
	var leitor io.Reader
	if corpo != nil {
		b, err := json.Marshal(corpo)
		if err != nil {
			c.t.Fatalf("marshal: %v", err)
		}
		leitor = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(c.t.Context(), metodo, c.srv.URL+rota, leitor)
	if err != nil {
		c.t.Fatalf("nova requisição: %v", err)
	}
	for k, v := range c.cabecalhos {
		req.Header[k] = v
	}
	if corpo != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", esquema+" "+token)
	}
	res, err := c.srv.Client().Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", metodo, rota, err)
	}
	defer func() { _ = res.Body.Close() }()
	r := resposta{status: res.StatusCode, header: res.Header}
	b, _ := io.ReadAll(res.Body)
	if len(b) > 0 {
		_ = json.Unmarshal(b, &r.corpo)
	}
	return r
}

func (c cliente) fazer(metodo, rota, token string, corpo any) resposta {
	c.t.Helper()
	return c.fazerComEsquema(metodo, rota, "Bearer", token, corpo)
}

func (c cliente) cadastrar(email, senha string) resposta {
	c.t.Helper()
	return c.fazer(http.MethodPost, "/usuarios", "", credenciais(email, senha))
}

func (c cliente) entrar(email, senha string) resposta {
	c.t.Helper()
	return c.fazer(http.MethodPost, "/login", "", credenciais(email, senha))
}

func credenciais(email, senha string) map[string]string {
	return map[string]string{"email": email, "senha": senha}
}

// subirAPI sobe a API real sobre Postgres e Redis de teste, com as regras
// padrão do limite de tentativas. ajustes alteram a configuração antes.
func subirAPI(t *testing.T, ajustes ...func(*api.Config)) (cliente, *pgxpool.Pool) {
	t.Helper()
	return subirAPIComRegras(t, limite.RegrasPadrao(), ajustes...)
}

// subirAPIComRegras é subirAPI com outras regras de limite de tentativas.
func subirAPIComRegras(t *testing.T, regras limite.Regras, ajustes ...func(*api.Config)) (cliente, *pgxpool.Pool) {
	t.Helper()
	pool := integracao.Postgres(t)
	opcoesRedis, prefixo := integracao.Redis(t)
	log := slog.New(slog.DiscardHandler)
	cfg := api.Config{
		Pool:       pool,
		SegredoJWT: []byte(segredoTeste),
		Log:        log,
		Tentativas: novasTentativas(t, opcoesRedis, limite.Config{Regras: regras, Prefixo: prefixo}),
	}
	for _, ajustar := range ajustes {
		ajustar(&cfg)
	}
	h, err := api.Novo(cfg)
	if err != nil {
		t.Fatalf("api.Novo: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return cliente{t: t, srv: srv}, pool
}

// novasTentativas monta as Tentativas e as fecha no fim do teste.
func novasTentativas(t *testing.T, opcoes *redis.Options, cfg limite.Config) *limite.Tentativas {
	t.Helper()
	tentativas := limite.Novo(opcoes, cfg, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = tentativas.Fechar() })
	return tentativas
}
