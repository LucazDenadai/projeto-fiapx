// Package mensageria fala com o RabbitMQ (ADR-0002).
package mensageria

import (
	"context"
	"fmt"
	"net"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Verificacao devolve a checagem de readiness do RabbitMQ: abre uma conexão
// AMQP (TCP + handshake) dentro do prazo do contexto e a fecha.
func Verificacao(url string) func(context.Context) error {
	return func(ctx context.Context) error {
		conn, err := amqp.DialConfig(url, amqp.Config{
			Dial: func(network, addr string) (net.Conn, error) {
				c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				prazo, ok := ctx.Deadline()
				if !ok {
					prazo = time.Now().Add(5 * time.Second)
				}
				// O prazo vale para o handshake; o amqp091 o limpa depois.
				if err := c.SetDeadline(prazo); err != nil {
					_ = c.Close()
					return nil, err
				}
				return c, nil
			},
		})
		if err != nil {
			return fmt.Errorf("conectar rabbitmq: %w", err)
		}
		_ = conn.Close()
		return nil
	}
}
