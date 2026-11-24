package integracao

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// ImagemMailpit é a mesma do docker-compose.yml.
const ImagemMailpit = "axllent/mailpit:v1.34"

var mailpitContainer container

// Mailpit é o servidor SMTP de teste: recebe os e-mails e os mostra pela API
// HTTP dele. O container é um por pacote; cada teste usa destinatários
// próprios e busca só os seus (EmailsPara).
type Mailpit struct {
	t *testing.T
	// SMTP é o host:porta do SMTP.
	SMTP string
	api  string
}

// NovoMailpit sobe (uma vez por pacote) o Mailpit. Com -short, pula o teste.
func NovoMailpit(t *testing.T) *Mailpit {
	t.Helper()
	pularSeCurto(t)
	enderecos, err := mailpitContainer.subir(func(ctx context.Context) (testcontainers.Container, string, error) {
		c, err := testcontainers.Run(ctx, ImagemMailpit,
			testcontainers.WithExposedPorts("1025/tcp", "8025/tcp"),
			testcontainers.WithWaitStrategy(
				wait.ForListeningPort("1025/tcp"),
				wait.ForHTTP("/readyz").WithPort("8025/tcp").WithStartupTimeout(60*time.Second),
			),
		)
		if err != nil {
			return c, "", fmt.Errorf("subir mailpit: %w", err)
		}
		smtp, err := c.PortEndpoint(ctx, "1025/tcp", "")
		if err != nil {
			return c, "", fmt.Errorf("smtp do mailpit: %w", err)
		}
		api, err := c.PortEndpoint(ctx, "8025/tcp", "http")
		if err != nil {
			return c, "", fmt.Errorf("api do mailpit: %w", err)
		}
		return c, smtp + " " + api, nil
	})
	if err != nil {
		t.Fatalf("mailpit de teste: %v", err)
	}
	smtp, api, _ := strings.Cut(enderecos, " ")
	return &Mailpit{t: t, SMTP: smtp, api: api}
}

// Email é o que o teste confere de um e-mail recebido.
type Email struct {
	Assunto   string
	Para      []string
	Texto     string
	HTML      string
	Cabecalho map[string][]string
}

// EmailsPara devolve os e-mails recebidos pelo endereço dado.
func (m *Mailpit) EmailsPara(endereco string) []Email {
	m.t.Helper()
	var busca struct {
		Messages []struct {
			ID string `json:"ID"`
		} `json:"messages"`
	}
	m.obter("/api/v1/search?query="+url.QueryEscape("to:"+endereco), &busca)
	var emails []Email
	for _, msg := range busca.Messages {
		var completo struct {
			Subject string
			To      []struct{ Address string }
			Text    string
			HTML    string
		}
		m.obter("/api/v1/message/"+msg.ID, &completo)
		var cab map[string][]string
		m.obter("/api/v1/message/"+msg.ID+"/headers", &cab)
		e := Email{Assunto: completo.Subject, Texto: completo.Text, HTML: completo.HTML, Cabecalho: cab}
		for _, p := range completo.To {
			e.Para = append(e.Para, p.Address)
		}
		emails = append(emails, e)
	}
	return emails
}

// EsperarEmailPara espera chegar ao menos um e-mail para o endereço.
func (m *Mailpit) EsperarEmailPara(endereco string, ate time.Duration) []Email {
	m.t.Helper()
	limite := time.Now().Add(ate)
	for {
		if e := m.EmailsPara(endereco); len(e) > 0 || time.Now().After(limite) {
			return e
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (m *Mailpit) obter(caminho string, destino any) {
	m.t.Helper()
	req, err := http.NewRequestWithContext(m.t.Context(), http.MethodGet, m.api+caminho, nil)
	if err != nil {
		m.t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		m.t.Fatalf("mailpit %s: %v", caminho, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		m.t.Fatalf("mailpit %s: status %d", caminho, res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(destino); err != nil {
		m.t.Fatalf("mailpit %s: %v", caminho, err)
	}
}
