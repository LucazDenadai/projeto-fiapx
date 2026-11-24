package mensageria

import (
	"context"
	"fmt"
	"net"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// discar abre uma conexão AMQP (TCP + handshake) dentro do prazo do contexto.
func discar(ctx context.Context, url string) (*amqp.Connection, error) {
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
		return nil, fmt.Errorf("conectar rabbitmq: %w", err)
	}
	return conn, nil
}
