package integracao

import (
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
)

// Proxy é um repasse TCP local para simular uma dependência fora do ar sem
// parar o container (que, ao voltar, ganharia outra porta).
type Proxy struct {
	t        *testing.T
	ln       net.Listener
	alvo     string
	mu       sync.Mutex
	cortado  bool
	conexoes map[net.Conn]struct{}
}

// NovoProxy repassa para o host:porta da URL dada e devolve a mesma URL
// apontando para o proxy.
func NovoProxy(t *testing.T, alvo string) (*Proxy, string) {
	t.Helper()
	u, err := url.Parse(alvo)
	if err != nil {
		t.Fatalf("url do alvo: %v", err)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy: %v", err)
	}
	p := &Proxy{t: t, ln: ln, alvo: u.Host, conexoes: map[net.Conn]struct{}{}}
	t.Cleanup(func() {
		_ = ln.Close()
		p.Cortar()
	})
	go p.aceitar()
	u.Host = ln.Addr().String()
	return p, u.String()
}

// Cortar derruba as conexões abertas e recusa as novas até Religar.
func (p *Proxy) Cortar() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cortado = true
	for c := range p.conexoes {
		_ = c.Close()
	}
	clear(p.conexoes)
}

// Religar volta a repassar as conexões novas.
func (p *Proxy) Religar() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cortado = false
}

func (p *Proxy) aceitar() {
	for {
		cliente, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.repassar(cliente)
	}
}

func (p *Proxy) repassar(cliente net.Conn) {
	destino, err := (&net.Dialer{}).DialContext(p.t.Context(), "tcp", p.alvo)
	if err != nil {
		_ = cliente.Close()
		return
	}
	// Confere o corte e registra sob o mesmo lock: um Cortar no meio do
	// caminho não deixa conexão viva.
	p.mu.Lock()
	if p.cortado {
		p.mu.Unlock()
		_, _ = cliente.Close(), destino.Close()
		return
	}
	p.conexoes[cliente], p.conexoes[destino] = struct{}{}, struct{}{}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.conexoes, cliente)
		delete(p.conexoes, destino)
		p.mu.Unlock()
	}()
	go func() { _, _ = io.Copy(destino, cliente); _ = destino.Close() }()
	_, _ = io.Copy(cliente, destino)
	_ = cliente.Close()
}
