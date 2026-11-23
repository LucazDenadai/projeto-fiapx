package api_test

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/LucazDenadai/projeto-fiapx/internal/api"
	"github.com/LucazDenadai/projeto-fiapx/internal/limite"
)

// exigir429 confere o 429 com Retry-After em segundos, no máximo a janela.
func exigir429(t *testing.T, r resposta, janela time.Duration) {
	t.Helper()
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, quero 429 (corpo %v)", r.status, r.corpo)
	}
	segundos, err := strconv.Atoi(r.header.Get("Retry-After"))
	if err != nil || segundos < 1 || time.Duration(segundos)*time.Second > janela {
		t.Errorf("Retry-After = %q, quero segundos entre 1 e %v", r.header.Get("Retry-After"), janela)
	}
}

func TestLimite_SextoLoginErradoDoMesmoEmailDa429(t *testing.T) {
	c, _ := subirAPI(t)
	c.cadastrar("ana@ex.com", "Forte#123")

	for i := range 5 {
		if r := c.entrar("ana@ex.com", "Errada#123"); r.status != http.StatusUnauthorized {
			t.Fatalf("tentativa %d: status = %d, quero 401", i+1, r.status)
		}
	}
	// Bloqueado mesmo com a senha certa e com o e-mail em outra caixa.
	exigir429(t, c.entrar("ANA@ex.com", "Forte#123"), 15*time.Minute)
}

func TestLimite_LoginComSucessoZeraOContadorDoEmail(t *testing.T) {
	c, _ := subirAPI(t)
	c.cadastrar("ana@ex.com", "Forte#123")

	for range 4 {
		c.entrar("ana@ex.com", "Errada#123")
	}
	if r := c.entrar("ana@ex.com", "Forte#123"); r.status != http.StatusOK {
		t.Fatalf("login certo: status = %d, quero 200", r.status)
	}
	// Zerado: mais 5 erros passam (401) antes do bloqueio.
	for i := range 5 {
		if r := c.entrar("ana@ex.com", "Errada#123"); r.status != http.StatusUnauthorized {
			t.Fatalf("erro %d depois do sucesso: status = %d, quero 401", i+1, r.status)
		}
	}
	exigir429(t, c.entrar("ana@ex.com", "Errada#123"), 15*time.Minute)
}

func TestLimite_LoginPorIPDa429DepoisDe20PorMinuto(t *testing.T) {
	c, _ := subirAPI(t)
	for i := range 20 {
		if r := c.entrar("ninguem"+strconv.Itoa(i)+"@ex.com", "Errada#123"); r.status != http.StatusUnauthorized {
			t.Fatalf("tentativa %d: status = %d, quero 401", i+1, r.status)
		}
	}
	exigir429(t, c.entrar("outro@ex.com", "Errada#123"), time.Minute)
}

func TestLimite_SextoCadastroDoMesmoIPDa429(t *testing.T) {
	c, _ := subirAPI(t)
	for i := range 5 {
		if r := c.cadastrar("u"+strconv.Itoa(i)+"@ex.com", "Forte#123"); r.status != http.StatusCreated {
			t.Fatalf("cadastro %d: status = %d, quero 201", i+1, r.status)
		}
	}
	exigir429(t, c.cadastrar("u5@ex.com", "Forte#123"), time.Hour)
}

func TestLimite_XForwardedForEhIgnorado(t *testing.T) {
	c, _ := subirAPI(t)
	for i := range 5 {
		c.cadastrar("u"+strconv.Itoa(i)+"@ex.com", "Forte#123")
	}
	for _, ip := range []string{"203.0.113.7", "198.51.100.1, 203.0.113.9"} {
		c.cabecalhos = http.Header{"X-Forwarded-For": {ip}}
		if r := c.cadastrar("xff@ex.com", "Forte#123"); r.status != http.StatusTooManyRequests {
			t.Errorf("X-Forwarded-For %q: status = %d, quero 429", ip, r.status)
		}
	}
}

// redisMudo aceita conexões e nunca responde: um Redis travado.
func redisMudo(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	return l.Addr().String()
}

func TestLimite_RedisForaDoArOuLentoDeixaPassarComLogDeErro(t *testing.T) {
	casos := map[string]func(t *testing.T) string{
		// Porta 1 do loopback: nada escuta ali, a conexão é recusada na hora.
		"fora do ar": func(*testing.T) string { return "127.0.0.1:1" },
		"lento":      redisMudo,
	}
	for nome, endereco := range casos {
		t.Run(nome, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			tentativas := limite.Novo(&redis.Options{Addr: endereco(t)}, limite.Config{Regras: limite.RegrasPadrao()}, log)
			t.Cleanup(func() { _ = tentativas.Fechar() })
			c, _ := subirAPI(t, func(cfg *api.Config) {
				cfg.Log = log
				cfg.Tentativas = tentativas
			})

			if r := c.cadastrar("ana@ex.com", "Forte#123"); r.status != http.StatusCreated {
				t.Fatalf("cadastro: status = %d, quero 201", r.status)
			}
			for range 6 {
				c.entrar("ana@ex.com", "Errada#123")
			}
			inicio := time.Now()
			if r := c.entrar("ana@ex.com", "Forte#123"); r.status != http.StatusOK {
				t.Errorf("login: status = %d, quero 200 (sem limite)", r.status)
			}
			if d := time.Since(inicio); d > 2*time.Second {
				t.Errorf("login levou %v: o Redis não pode segurar o login", d)
			}
			if !strings.Contains(logs.String(), `"level":"ERROR","msg":"limite de tentativas: redis indisponível`) {
				t.Errorf("quero log de erro do Redis indisponível; logs:\n%s", logs.String())
			}
		})
	}
}
