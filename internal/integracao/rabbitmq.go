package integracao

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"
)

// ImagemRabbitMQ é a mesma do docker-compose.yml.
const ImagemRabbitMQ = "rabbitmq:4.2-alpine"

var rabbitContainer container

// RabbitMQ cria um vhost exclusivo do teste e devolve a URL AMQP dele. Com
// -short, pula o teste.
func RabbitMQ(t *testing.T) string {
	t.Helper()
	pularSeCurto(t)
	base, err := rabbitContainer.subir(func(ctx context.Context) (testcontainers.Container, string, error) {
		c, err := rabbitmq.Run(ctx, ImagemRabbitMQ)
		if err != nil {
			return c, "", fmt.Errorf("subir rabbitmq: %w", err)
		}
		u, err := c.AmqpURL(ctx)
		if err != nil {
			return c, "", fmt.Errorf("url do rabbitmq: %w", err)
		}
		return c, u, nil
	})
	if err != nil {
		t.Fatalf("rabbitmq de teste: %v", err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("url do rabbitmq: %v", err)
	}
	vhost := sufixo()
	usuario := u.User.Username()
	for _, cmd := range [][]string{
		{"rabbitmqctl", "add_vhost", vhost},
		{"rabbitmqctl", "set_permissions", "-p", vhost, usuario, ".*", ".*", ".*"},
	} {
		codigo, saida, err := rabbitContainer.ativo.Exec(t.Context(), cmd)
		if err != nil || codigo != 0 {
			msg, _ := io.ReadAll(saida)
			t.Fatalf("%v: código %d, %v: %s", cmd, codigo, err, msg)
		}
	}
	u.Path = "/" + vhost
	return u.String()
}
