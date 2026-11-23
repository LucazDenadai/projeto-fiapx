package banco_test

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
)

// ImagemPostgres é a mesma do docker-compose.yml (ADR-0016).
const imagemPostgres = "postgres:18-alpine"

func subirPostgres(t *testing.T) (*postgres.PostgresContainer, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("teste de integração: pulado com -short")
	}
	ctx := context.Background()
	c, err := postgres.Run(ctx, imagemPostgres,
		postgres.WithDatabase("fiapx"),
		postgres.WithUsername("fiapx"),
		postgres.WithPassword("fiapx"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("subir postgres: %v", err)
	}
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return c, url
}

func TestMigrar_AplicaAsMigrationsEhIdempotente(t *testing.T) {
	_, url := subirPostgres(t)
	ctx := context.Background()

	pool, err := banco.Abrir(ctx, url)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	defer pool.Close()

	if err := banco.Migrar(ctx, pool); err != nil {
		t.Fatalf("primeira migração: %v", err)
	}
	if err := banco.Migrar(ctx, pool); err != nil {
		t.Fatalf("segunda migração deveria ser no-op: %v", err)
	}

	var versao int64
	if err := pool.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version`).Scan(&versao); err != nil {
		t.Fatalf("ler versão do goose: %v", err)
	}
	if versao < 1 {
		t.Errorf("versão do schema = %d, quero ao menos 1", versao)
	}
}

func TestVerificar_FalhaComPostgresParado(t *testing.T) {
	c, url := subirPostgres(t)
	ctx := context.Background()

	pool, err := banco.Abrir(ctx, url)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	defer pool.Close()
	verificar := banco.Verificacao(pool)

	if err := verificar(ctx); err != nil {
		t.Fatalf("postgres no ar deveria passar: %v", err)
	}

	parada := 5 * time.Second
	if err := c.Stop(ctx, &parada); err != nil {
		t.Fatalf("parar postgres: %v", err)
	}
	ctxCurto, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := verificar(ctxCurto); err == nil {
		t.Fatal("postgres parado deveria falhar a verificação")
	}
}
