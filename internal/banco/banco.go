// Package banco abre a conexão com o Postgres compartilhado por API e Worker
// (ADR-0001), aplica as migrations e informa se o banco responde.
package banco

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/LucazDenadai/projeto-fiapx/migrations"
)

// Abrir cria o pool de conexões. Não exige o banco no ar: a disponibilidade
// aparece no /readyz.
func Abrir(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("abrir pool do postgres: %w", err)
	}
	return pool, nil
}

// Migrar aplica as migrations embutidas que faltam. Aplicar de novo é no-op.
func Migrar(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("preparar migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("aplicar migrations: %w", err)
	}
	return nil
}

// Verificacao devolve a checagem de readiness do Postgres.
func Verificacao(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return fmt.Errorf("ping postgres: %w", err)
		}
		return nil
	}
}
