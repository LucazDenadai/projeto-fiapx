// Package integracao sobe dependências reais em testcontainers para os testes
// de integração (ADR-0016). Só código de teste importa este pacote.
//
// Uso: o TestMain do pacote chama Principal. Cada teste chama Postgres ou
// Redis: o container sobe uma vez por pacote, no primeiro uso, e cada teste
// recebe um estado só dele (schema no Postgres, prefixo de chave no Redis).
package integracao

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
)

// ImagemPostgres é a mesma do docker-compose.yml.
const ImagemPostgres = "postgres:18-alpine"

// container sobe sob demanda, uma vez por pacote, e é derrubado por Principal.
type container struct {
	once  sync.Once
	url   string
	err   error
	ativo testcontainers.Container
}

var (
	mu          sync.Mutex
	encerrar    []testcontainers.Container
	pgContainer container
)

func (c *container) subir(subir func(ctx context.Context) (testcontainers.Container, string, error)) (string, error) {
	c.once.Do(func() {
		c.ativo, c.url, c.err = subir(context.Background())
		if c.ativo != nil {
			mu.Lock()
			encerrar = append(encerrar, c.ativo)
			mu.Unlock()
		}
	})
	return c.url, c.err
}

// Principal é o corpo do TestMain de um pacote com testes de integração: roda
// os testes e derruba os containers que eles subiram.
func Principal(m *testing.M) {
	codigo := m.Run()
	mu.Lock()
	for _, c := range encerrar {
		_ = testcontainers.TerminateContainer(c)
	}
	mu.Unlock()
	os.Exit(codigo)
}

func pularSeCurto(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("teste de integração: pulado com -short")
	}
}

// sufixo devolve um identificador aleatório, válido como nome de schema.
func sufixo() string {
	return "t_" + strings.ToLower(rand.Text())
}

// Postgres cria um schema exclusivo do teste, aplica as migrations nele e
// devolve um pool que só enxerga esse schema. Com -short, pula o teste.
func Postgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pularSeCurto(t)
	url, err := pgContainer.subir(func(ctx context.Context) (testcontainers.Container, string, error) {
		c, err := postgres.Run(ctx, ImagemPostgres,
			postgres.WithDatabase("fiapx"),
			postgres.WithUsername("fiapx"),
			postgres.WithPassword("fiapx"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
		)
		if err != nil {
			return c, "", fmt.Errorf("subir postgres: %w", err)
		}
		url, err := c.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			return c, "", fmt.Errorf("url do postgres: %w", err)
		}
		return c, url, nil
	})
	if err != nil {
		t.Fatalf("postgres de teste: %v", err)
	}

	ctx := t.Context()
	schema := sufixo()
	admin, err := banco.Abrir(ctx, url)
	if err != nil {
		t.Fatalf("abrir pool: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("criar schema: %v", err)
	}

	pool, err := banco.Abrir(ctx, url+"&search_path="+schema)
	if err != nil {
		t.Fatalf("abrir pool do schema: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := banco.Migrar(ctx, pool); err != nil {
		t.Fatalf("migrar: %v", err)
	}
	return pool
}
