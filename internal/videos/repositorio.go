package videos

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Destino da mensagem do Job: a fila processamento, pela exchange padrão
// (ADR-0002). A topologia é declarada pelo relay (#25).
const (
	exchangeJob   = ""
	routingKeyJob = "processamento"
)

type repositorio struct {
	pool *pgxpool.Pool
}

const colunas = `id, nome_original, status, coalesce(motivo, ''), criado_em, atualizado_em, zip_expira_em`

func escanear(row pgx.Row) (Video, error) {
	var v Video
	err := row.Scan(&v.ID, &v.NomeOriginal, &v.Status, &v.Motivo, &v.CriadoEm, &v.AtualizadoEm, &v.ZipExpiraEm)
	return v, err
}

func (r *repositorio) criar(ctx context.Context, usuarioID, id uuid.UUID, nome string, correlationID uuid.UUID) (Video, error) {
	payload, err := json.Marshal(map[string]string{"video_id": id.String(), "correlation_id": correlationID.String()})
	if err != nil {
		return Video{}, fmt.Errorf("montar mensagem do job: %w", err)
	}
	var v Video
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		v, err = escanear(tx.QueryRow(ctx, `INSERT INTO videos (id, usuario_id, nome_original, status)
			VALUES ($1, $2, $3, $4) RETURNING `+colunas, id, usuarioID, nome, StatusRecebido))
		if err != nil {
			return fmt.Errorf("inserir vídeo: %w", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO outbox (exchange, routing_key, payload) VALUES ($1, $2, $3)`,
			exchangeJob, routingKeyJob, payload)
		if err != nil {
			return fmt.Errorf("inserir outbox: %w", err)
		}
		return nil
	})
	return v, err
}

func (r *repositorio) buscar(ctx context.Context, usuarioID, id uuid.UUID) (Video, error) {
	v, err := escanear(r.pool.QueryRow(ctx,
		`SELECT `+colunas+` FROM videos WHERE id = $1 AND usuario_id = $2`, id, usuarioID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Video{}, ErrNaoEncontrado
	}
	if err != nil {
		return Video{}, fmt.Errorf("buscar vídeo: %w", err)
	}
	return v, nil
}

func (r *repositorio) listar(ctx context.Context, usuarioID uuid.UUID, depois *posicao, n int) ([]Video, error) {
	consulta := `SELECT ` + colunas + ` FROM videos WHERE usuario_id = $1`
	args := []any{usuarioID}
	if depois != nil {
		consulta += ` AND (criado_em, id) < ($2, $3)`
		args = append(args, depois.criadoEm, depois.id)
	}
	consulta += fmt.Sprintf(` ORDER BY criado_em DESC, id DESC LIMIT %d`, n)
	rows, err := r.pool.Query(ctx, consulta, args...)
	if err != nil {
		return nil, fmt.Errorf("listar vídeos: %w", err)
	}
	lista, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Video, error) { return escanear(row) })
	if err != nil {
		return nil, fmt.Errorf("listar vídeos: %w", err)
	}
	return lista, nil
}

// posicao é onde a próxima página começa: depois deste (criado_em, id).
type posicao struct {
	criadoEm time.Time
	id       uuid.UUID
}

func codificarCursor(p posicao) string {
	bruto := p.criadoEm.UTC().Format(time.RFC3339Nano) + "|" + p.id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(bruto))
}

func decodificarCursor(cursor string) (posicao, error) {
	bruto, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return posicao{}, err
	}
	data, id, ok := strings.Cut(string(bruto), "|")
	if !ok {
		return posicao{}, errors.New("cursor sem separador")
	}
	t, err := time.Parse(time.RFC3339Nano, data)
	if err != nil {
		return posicao{}, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return posicao{}, err
	}
	return posicao{criadoEm: t, id: u}, nil
}
