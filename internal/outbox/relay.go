// Package outbox publica no RabbitMQ as mensagens gravadas na tabela outbox
// junto com cada mudança de estado (ADR-0003).
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
)

// Publicador é o que o relay precisa do broker.
type Publicador interface {
	// Conectar garante a conexão e a topologia declarada.
	Conectar(ctx context.Context) error
	// Publicar só volta sem erro depois do confirm do broker.
	Publicar(ctx context.Context, exchange, routingKey, id string, corpo []byte) error
}

// Config do relay; zero em um campo usa o padrão.
type Config struct {
	Intervalo         time.Duration // entre rodadas sem pendência (padrão 1 s)
	Lote              int           // linhas por rodada (padrão 100)
	BackoffMax        time.Duration // teto da espera após falha (padrão 30 s)
	TimeoutPublicacao time.Duration // por mensagem (padrão 10 s)
}

func (c Config) comPadroes() Config {
	if c.Intervalo <= 0 {
		c.Intervalo = time.Second
	}
	if c.Lote <= 0 {
		c.Lote = 100
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	if c.TimeoutPublicacao <= 0 {
		c.TimeoutPublicacao = 10 * time.Second
	}
	return c
}

// Relay faz polling da outbox e publica as linhas pendentes. Réplicas (API e
// Worker, várias de cada) não disputam linhas: o lote é travado com SKIP
// LOCKED.
type Relay struct {
	pool *pgxpool.Pool
	pub  Publicador
	cfg  Config
	log  *slog.Logger
}

// NovoRelay monta o relay; ele só roda em Iniciar ou Rodada.
func NovoRelay(pool *pgxpool.Pool, pub Publicador, cfg Config, log *slog.Logger) *Relay {
	return &Relay{pool: pool, pub: pub, cfg: cfg.comPadroes(), log: log}
}

// Subir liga o relay de um processo (API ou Worker) ao RabbitMQ da URL dada e
// o põe para rodar. A função devolvida para o relay, espera e fecha a
// conexão; chame-a antes de fechar o pool.
func Subir(ctx context.Context, pool *pgxpool.Pool, amqpURL string, cfg Config, log *slog.Logger) (parar func()) {
	pub := mensageria.NovoPublicador(amqpURL)
	ctx, cancelar := context.WithCancel(ctx)
	esperar := NovoRelay(pool, pub, cfg, log).Iniciar(ctx)
	return func() {
		cancelar()
		esperar()
		pub.Fechar()
	}
}

// Iniciar roda o relay em segundo plano até o ctx acabar. A função devolvida
// espera ele parar.
func (r *Relay) Iniciar(ctx context.Context) (esperar func()) {
	var wg sync.WaitGroup
	wg.Go(func() { r.rodar(ctx) })
	return wg.Wait
}

func (r *Relay) rodar(ctx context.Context) {
	falhas := 0
	for {
		n, err := r.Rodada(ctx)
		var pausa time.Duration
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			falhas++
			pausa = r.backoff(falhas)
			r.log.WarnContext(ctx, "relay da outbox: tentando de novo", "erro", err, "falhas", falhas, "espera", pausa)
		case n == r.cfg.Lote:
			falhas = 0 // lote cheio: pode haver mais, segue direto
		default:
			falhas = 0
			pausa = r.cfg.Intervalo
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pausa):
		}
	}
}

// backoff dobra a espera a cada falha seguida (1 s, 2 s, 4 s…) até o teto.
func (r *Relay) backoff(falhas int) time.Duration {
	espera := time.Second
	for range falhas - 1 {
		espera *= 2
		if espera >= r.cfg.BackoffMax {
			return r.cfg.BackoffMax
		}
	}
	return min(espera, r.cfg.BackoffMax)
}

type linha struct {
	id                   int64
	exchange, routingKey string
	payload              []byte
}

// Rodada publica um lote de linhas pendentes e devolve quantas publicou. Uma
// linha recusada pelo broker (sem rota ou nack) é pulada, para não travar as
// outras (não há garantia de ordem); falha de conexão para o lote. Em ambos os
// casos o que já foi confirmado recebe publicado_em e o resto fica pendente.
func (r *Relay) Rodada(ctx context.Context) (int, error) {
	if err := r.pub.Conectar(ctx); err != nil {
		return 0, err
	}
	// O banco não segue o cancelamento: parar no meio do lote não pode
	// desfazer o publicado_em do que o broker já confirmou.
	ctxBanco := context.WithoutCancel(ctx)
	publicadas := 0
	var falha error
	err := pgx.BeginFunc(ctxBanco, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctxBanco, `SELECT id, exchange, routing_key, payload FROM outbox
			WHERE publicado_em IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, r.cfg.Lote)
		if err != nil {
			return fmt.Errorf("ler outbox: %w", err)
		}
		lote, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (linha, error) {
			var l linha
			err := row.Scan(&l.id, &l.exchange, &l.routingKey, &l.payload)
			return l, err
		})
		if err != nil {
			return fmt.Errorf("ler outbox: %w", err)
		}
		var ids []int64
		for _, l := range lote {
			err := r.publicar(ctx, l)
			if err == nil {
				ids = append(ids, l.id)
				continue
			}
			falha = err
			if !errors.Is(err, mensageria.ErrSemRota) && !errors.Is(err, mensageria.ErrRecusada) {
				break
			}
			r.log.ErrorContext(ctx, "linha da outbox recusada pelo broker", "outbox_id", l.id, "erro", err)
		}
		if len(ids) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctxBanco, `UPDATE outbox SET publicado_em = now() WHERE id = ANY($1)`, ids); err != nil {
			return fmt.Errorf("marcar publicadas: %w", err)
		}
		publicadas = len(ids)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return publicadas, falha
}

func (r *Relay) publicar(ctx context.Context, l linha) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.TimeoutPublicacao)
	defer cancel()
	if err := r.pub.Publicar(ctx, l.exchange, l.routingKey, strconv.FormatInt(l.id, 10), l.payload); err != nil {
		return fmt.Errorf("publicar linha %d da outbox: %w", l.id, err)
	}
	return nil
}
