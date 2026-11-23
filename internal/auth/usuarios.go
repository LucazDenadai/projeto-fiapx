package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Usuarios guarda os Usuários. A implementação usada pela API é a do Postgres
// (NovoRepositorio).
type Usuarios interface {
	// Criar grava o Usuário. Devolve ErrEmailEmUso se o e-mail já existe.
	Criar(ctx context.Context, email, senhaHash string) error
	// BuscarPorEmail devolve o id e o hash. Devolve errNaoEncontrado se não existe.
	BuscarPorEmail(ctx context.Context, email string) (id, senhaHash string, err error)
}

var errNaoEncontrado = errors.New("usuário não encontrado")

type repositorio struct {
	pool *pgxpool.Pool
}

// NovoRepositorio devolve Usuarios sobre a tabela usuarios do Postgres.
func NovoRepositorio(pool *pgxpool.Pool) Usuarios {
	return repositorio{pool: pool}
}

const violacaoUnica = "23505"

func (r repositorio) Criar(ctx context.Context, email, senhaHash string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO usuarios (email, senha_hash) VALUES ($1, $2)`, email, senhaHash)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == violacaoUnica {
		return ErrEmailEmUso
	}
	if err != nil {
		return fmt.Errorf("inserir usuário: %w", err)
	}
	return nil
}

func (r repositorio) BuscarPorEmail(ctx context.Context, email string) (string, string, error) {
	var id, hash string
	err := r.pool.QueryRow(ctx, `SELECT id::text, senha_hash FROM usuarios WHERE email = $1`, email).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errNaoEncontrado
	}
	if err != nil {
		return "", "", fmt.Errorf("buscar usuário: %w", err)
	}
	return id, hash, nil
}
