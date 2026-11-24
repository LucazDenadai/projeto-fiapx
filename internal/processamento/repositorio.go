package processamento

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/videos"
)

var (
	errNaoEncontrado = errors.New("vídeo não encontrado")
	errTerminal      = errors.New("vídeo já terminal")
)

type repositorio struct {
	pool *pgxpool.Pool
}

// iniciar marca o Vídeo processando. Aceita processando → processando: é a
// reentrega depois de uma queda, que recomeça do zero (ADR-0014). Vídeo
// terminal dá errTerminal; inexistente, errNaoEncontrado.
func (r repositorio) iniciar(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `UPDATE videos SET status = $2, atualizado_em = now()
		WHERE id = $1 AND status IN ($3, $2)`, id, videos.StatusProcessando, videos.StatusRecebido)
	if err != nil {
		return fmt.Errorf("marcar processando: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var existe bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM videos WHERE id = $1)`, id).Scan(&existe); err != nil {
		return fmt.Errorf("buscar vídeo: %w", err)
	}
	if !existe {
		return errNaoEncontrado
	}
	return errTerminal
}

// concluir marca o Vídeo concluído, com o prazo do zip, e grava o evento
// concluido na outbox, na mesma transação (ADR-0003). Devolve false, sem
// evento, se o Vídeo já não estava processando (outra entrega terminou).
func (r repositorio) concluir(ctx context.Context, id uuid.UUID, correlationID string, retencao time.Duration) (bool, error) {
	concluiu := false
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var ocorridoEm time.Time
		err := tx.QueryRow(ctx, `UPDATE videos
			SET status = $2, atualizado_em = now(), zip_expira_em = now() + make_interval(secs => $4)
			WHERE id = $1 AND status = $3 RETURNING atualizado_em`,
			id, videos.StatusConcluido, videos.StatusProcessando, retencao.Seconds()).Scan(&ocorridoEm)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("marcar concluído: %w", err)
		}
		payload, err := json.Marshal(map[string]any{
			"video_id":       id,
			"correlation_id": correlationID,
			"ocorrido_em":    ocorridoEm,
		})
		if err != nil {
			return fmt.Errorf("montar evento: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO outbox (exchange, routing_key, payload) VALUES ($1, $2, $3)`,
			mensageria.ExchangeEventos, mensageria.EventoConcluido, payload); err != nil {
			return fmt.Errorf("inserir evento na outbox: %w", err)
		}
		concluiu = true
		return nil
	})
	return concluiu, err
}
