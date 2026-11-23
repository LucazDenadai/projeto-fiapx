package integracao

import (
	"context"
	"fmt"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redis"
)

// ImagemRedis é a mesma do docker-compose.yml.
const ImagemRedis = "redis:8.4-alpine"

var redisContainer container

// Redis devolve as opções de conexão do Redis de teste e um prefixo de chave
// exclusivo do teste. Com -short, pula o teste.
func Redis(t *testing.T) (*goredis.Options, string) {
	t.Helper()
	pularSeCurto(t)
	url, err := redisContainer.subir(func(ctx context.Context) (testcontainers.Container, string, error) {
		c, err := redis.Run(ctx, ImagemRedis)
		if err != nil {
			return c, "", fmt.Errorf("subir redis: %w", err)
		}
		url, err := c.ConnectionString(ctx)
		if err != nil {
			return c, "", fmt.Errorf("url do redis: %w", err)
		}
		return c, url, nil
	})
	if err != nil {
		t.Fatalf("redis de teste: %v", err)
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatalf("url do redis: %v", err)
	}
	return opts, sufixo() + ":"
}
