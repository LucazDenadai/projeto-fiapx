package mensageria_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
)

// imagemRabbitMQ é a mesma do docker-compose.yml (ADR-0016).
const imagemRabbitMQ = "rabbitmq:4.2-alpine"

func TestVerificacao_FalhaComRabbitMQParado(t *testing.T) {
	if testing.Short() {
		t.Skip("teste de integração: pulado com -short")
	}
	ctx := context.Background()
	c, err := rabbitmq.Run(ctx, imagemRabbitMQ)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("subir rabbitmq: %v", err)
	}
	url, err := c.AmqpURL(ctx)
	if err != nil {
		t.Fatalf("url amqp: %v", err)
	}
	verificar := mensageria.Verificacao(url)

	if err := verificar(ctx); err != nil {
		t.Fatalf("rabbitmq no ar deveria passar: %v", err)
	}

	parada := 10 * time.Second
	if err := c.Stop(ctx, &parada); err != nil {
		t.Fatalf("parar rabbitmq: %v", err)
	}
	ctxCurto, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := verificar(ctxCurto); err == nil {
		t.Fatal("rabbitmq parado deveria falhar a verificação")
	}
}
