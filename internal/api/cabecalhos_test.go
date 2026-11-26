package api_test

import (
	"net/http"
	"testing"
	"time"
)

// Toda resposta da API leva os cabeçalhos de segurança do OWASP REST Security
// Cheat Sheet, inclusive erros e rotas que não existem. O JSON não fica em
// cache (o login devolve o token); o zip mantém a própria política.
func TestCabecalhos_DeSegurancaEmTodaResposta(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	id := c.enviarVideo(ana, "ferias.mp4")
	c.concluir(pool, id, time.Now().Add(time.Hour))

	casos := map[string]http.Header{
		"login":        c.entrar("ana@ex.com", "Forte#123").header,
		"sem token":    c.fazer(http.MethodGet, "/videos", "", nil).header,
		"listagem":     c.fazer(http.MethodGet, "/videos", ana, nil).header,
		"inexistente":  c.fazer(http.MethodGet, "/nada", "", nil).header,
		"zip":          c.baixar(ana, id, nil).header,
		"zip com erro": c.baixar(ana, "00000000-0000-0000-0000-000000000000", nil).header,
	}
	for nome, h := range casos {
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, quero nosniff", nome, got)
		}
		if got := h.Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
			t.Errorf("%s: Cross-Origin-Resource-Policy = %q, quero same-origin", nome, got)
		}
		quero := "no-store"
		if nome == "zip" {
			quero = "private, no-cache"
		}
		if got := h.Get("Cache-Control"); got != quero {
			t.Errorf("%s: Cache-Control = %q, quero %q", nome, got, quero)
		}
	}
}
