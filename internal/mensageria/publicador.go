package mensageria

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Erros de publicação: a mensagem não foi aceita pelo broker.
var (
	ErrSemRota  = errors.New("mensagem sem rota")
	ErrRecusada = errors.New("mensagem recusada pelo broker (nack)")
)

// Publicador publica com mandatory e publisher confirm (ADR-0003). Reconecta
// sozinho na próxima chamada depois de uma queda e, a cada conexão, declara a
// topologia. Uma publicação por vez.
type Publicador struct {
	url string

	mu       sync.Mutex
	conn     *amqp.Connection
	ch       *amqp.Channel
	retornos chan amqp.Return
}

// NovoPublicador não conecta; a conexão abre em Conectar ou Publicar.
func NovoPublicador(url string) *Publicador {
	return &Publicador{url: url}
}

// Conectar garante a conexão, com a topologia declarada.
func (p *Publicador) Conectar(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conectar(ctx)
}

func (p *Publicador) conectar(ctx context.Context) error {
	if p.ch != nil && !p.ch.IsClosed() && !p.conn.IsClosed() {
		return nil
	}
	p.fechar()
	conn, err := discar(ctx, p.url)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err == nil {
		err = ch.Confirm(false)
	}
	if err == nil {
		err = declararTopologia(ch)
	}
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("preparar canal: %w", err)
	}
	p.conn, p.ch = conn, ch
	// Com mandatory, o basic.return chega antes do ack da mesma mensagem.
	p.retornos = ch.NotifyReturn(make(chan amqp.Return, 1))
	return nil
}

// Publicar envia a mensagem persistente e espera o confirm. Devolve erro se o
// broker estiver fora, recusar (nack) ou não tiver rota para ela.
func (p *Publicador) Publicar(ctx context.Context, exchange, routingKey, id string, corpo []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.conectar(ctx); err != nil {
		return err
	}
	confirmacao, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, exchange, routingKey, true, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    id,
		Timestamp:    time.Now(),
		Body:         corpo,
	})
	if err != nil {
		p.fechar()
		return fmt.Errorf("publicar: %w", err)
	}
	ok, err := confirmacao.WaitContext(ctx)
	if err != nil {
		// Sem saber o estado do canal, começa de novo na próxima.
		p.fechar()
		return fmt.Errorf("esperar confirm: %w", err)
	}
	select {
	case r := <-p.retornos:
		return fmt.Errorf("%w: %s (%s → %s)", ErrSemRota, r.ReplyText, r.Exchange, r.RoutingKey)
	default:
	}
	if !ok {
		return ErrRecusada
	}
	return nil
}

// Fechar encerra a conexão, se houver.
func (p *Publicador) Fechar() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fechar()
}

func (p *Publicador) fechar() {
	if p.conn != nil {
		_ = p.conn.Close()
	}
	p.conn, p.ch, p.retornos = nil, nil, nil
}
