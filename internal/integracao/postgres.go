// Package integracao sobe dependências reais em testcontainers para os testes
// de integração (ADR-0016). Só código de teste importa este pacote.
//
// Uso: o TestMain do pacote chama Principal, que sobe um Postgres para o
// pacote todo; cada teste chama Postgres, que cria um schema só dele com as
// migrations aplicadas.
package integracao

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
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

// urlPostgres é preenchida por Principal; vazia com -short.
var urlPostgres string

// Principal é o corpo do TestMain de um pacote com testes de integração: sobe
// o Postgres (a não ser com -short), roda os testes e derruba o container.
func Principal(m *testing.M) {
	os.Exit(principal(m))
}

func principal(m *testing.M) int {
	ctx := context.Background()
	if !curto() {
		c, err := postgres.Run(ctx, ImagemPostgres,
			postgres.WithDatabase("fiapx"),
			postgres.WithUsername("fiapx"),
			postgres.WithPassword("fiapx"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
		)
		defer func() { _ = testcontainers.TerminateContainer(c) }()
		if err != nil {
			fmt.Fprintf(os.Stderr, "subir postgres: %v\n", err)
			return 1
		}
		urlPostgres, err = c.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			fmt.Fprintf(os.Stderr, "connection string: %v\n", err)
			return 1
		}
	}
	return m.Run()
}

// curto lê -short antes de m.Run (testing.Short exige flag.Parse).
func curto() bool {
	for _, a := range os.Args[1:] {
		if a == "-test.short" || a == "-test.short=true" {
			return true
		}
	}
	return false
}

// Postgres cria um schema exclusivo do teste, aplica as migrations nele e
// devolve um pool que só enxerga esse schema. Com -short, pula o teste.
func Postgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("teste de integração: pulado com -short")
	}
	if urlPostgres == "" {
		t.Fatal("Postgres de teste não está no ar: o TestMain do pacote precisa chamar integracao.Principal")
	}
	ctx := t.Context()
	schema := "t_" + strings.ToLower(rand.Text())

	admin, err := banco.Abrir(ctx, urlPostgres)
	if err != nil {
		t.Fatalf("abrir pool: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("criar schema: %v", err)
	}

	pool, err := banco.Abrir(ctx, urlPostgres+"&search_path="+schema)
	if err != nil {
		t.Fatalf("abrir pool do schema: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := banco.Migrar(ctx, pool); err != nil {
		t.Fatalf("migrar: %v", err)
	}
	return pool
}
