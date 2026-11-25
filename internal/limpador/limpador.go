// Package limpador recolhe, no Worker, o que o volume e a outbox acumulam:
// zips vencidos, originais e temporários que sobraram, diretórios sem Vídeo e
// linhas já publicadas (ADR-0015).
package limpador

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/videos"
)

// Padrões do ADR-0015.
const (
	IntervaloPadrao      = time.Hour
	IdadeOrfaoPadrao     = time.Hour
	RetencaoOutboxPadrao = 7 * 24 * time.Hour
	loteOutboxPadrao     = 1000
)

const (
	// chaveLock é a do pg_try_advisory_lock: a mesma em todas as réplicas.
	chaveLock = 0x66696170785f6c // "fiapx_l"
	// loteVideos é quantos diretórios são conferidos no banco por consulta.
	loteVideos         = 1000
	timeoutLiberarLock = 5 * time.Second
)

// Armazenamento é o que o limpador precisa do volume (ADR-0005).
type Armazenamento interface {
	Diretorios(ctx context.Context) ([]armazenamento.Diretorio, error)
	Apagar(ctx context.Context, id uuid.UUID) error
	ApagarOriginal(ctx context.Context, id uuid.UUID) error
	ApagarZip(ctx context.Context, id uuid.UUID) error
	ApagarSeVazio(ctx context.Context, id uuid.UUID) error
	ApagarTemporarios(ctx context.Context, limite time.Time) (int, error)
}

// Config do Limpador; zero em um campo usa o padrão.
type Config struct {
	Intervalo time.Duration // entre rodadas (padrão 1h)
	// IdadeOrfao é há quanto tempo um temporário ou um diretório sem Vídeo
	// precisa estar parado para ser apagado (padrão 1h). Tem de ficar acima do
	// timeout de envio da API: abaixo, um envio em andamento parece órfão.
	IdadeOrfao     time.Duration
	RetencaoOutbox time.Duration // das linhas publicadas (padrão 7 dias)
	LoteOutbox     int           // linhas apagadas por vez (padrão 1000)
	Agora          func() time.Time
	// Registro recebe as métricas do limpador; nil não registra.
	Registro prometheus.Registerer
}

func (c Config) comPadroes() Config {
	if c.Intervalo <= 0 {
		c.Intervalo = IntervaloPadrao
	}
	if c.IdadeOrfao <= 0 {
		c.IdadeOrfao = IdadeOrfaoPadrao
	}
	if c.RetencaoOutbox <= 0 {
		c.RetencaoOutbox = RetencaoOutboxPadrao
	}
	if c.LoteOutbox <= 0 {
		c.LoteOutbox = loteOutboxPadrao
	}
	if c.Agora == nil {
		c.Agora = time.Now
	}
	return c
}

// Limpador roda de tempos em tempos em cada Worker; um advisory lock do
// Postgres deixa só uma réplica por rodada.
type Limpador struct {
	pool *pgxpool.Pool
	arqs Armazenamento
	cfg  Config
	met  *metricas
	log  *slog.Logger
}

// Novo monta o Limpador; ele só roda em Iniciar ou Rodada.
func Novo(pool *pgxpool.Pool, arqs Armazenamento, cfg Config, log *slog.Logger) *Limpador {
	return &Limpador{pool: pool, arqs: arqs, cfg: cfg.comPadroes(), met: novasMetricas(cfg.Registro), log: log}
}

// Iniciar faz uma rodada agora e outra a cada intervalo, em segundo plano, até
// o ctx acabar. A função devolvida espera ele parar; chame-a antes de fechar o
// pool.
func (l *Limpador) Iniciar(ctx context.Context) (esperar func()) {
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			l.rodar(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(l.cfg.Intervalo):
			}
		}
	})
	return wg.Wait
}

func (l *Limpador) rodar(ctx context.Context) {
	inicio := time.Now()
	r, err := l.Rodada(ctx)
	if ctx.Err() != nil {
		return
	}
	log := l.log.With("zips", r.Zips, "originais", r.Originais, "temporarios", r.Temporarios,
		"orfaos", r.Orfaos, "outbox", r.Outbox, "duracao", time.Since(inicio).Round(time.Millisecond).String())
	switch {
	case err != nil:
		// O que faltou fica para a próxima rodada.
		log.WarnContext(ctx, "limpador: rodada com erro", "erro", err)
	case !r.Executada:
		l.log.InfoContext(ctx, "limpador: rodada pulada, outra réplica está limpando")
	default:
		log.InfoContext(ctx, "limpador: rodada concluída")
	}
}

// Resultado diz quantos itens de cada tipo uma rodada recolheu.
type Resultado struct {
	Executada   bool // false: outra réplica está com a rodada
	Zips        int
	Originais   int
	Temporarios int
	Orfaos      int // diretórios sem Vídeo
	Outbox      int // linhas publicadas
}

// Rodada recolhe tudo o que venceu. Se outra réplica já está limpando, pula,
// sem erro. A falha num item não impede os outros: volta no erro, e a próxima
// rodada tenta de novo.
func (l *Limpador) Rodada(ctx context.Context) (r Resultado, err error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return r, fmt.Errorf("conectar ao postgres: %w", err)
	}
	// O lock é da sessão: as consultas da rodada usam a mesma conexão, e se ela
	// cair, o Postgres libera o lock.
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(chaveLock)).Scan(&r.Executada); err != nil {
		descartar(ctx, conn)
		return r, fmt.Errorf("pegar o lock do limpador: %w", err)
	}
	if !r.Executada {
		conn.Release()
		return r, nil
	}
	defer func() { err = errors.Join(err, liberarLock(ctx, conn)) }()

	inicio := time.Now()
	// Mesmo com erro, o que foi apagado conta.
	defer func() {
		l.met.contar(r)
		l.met.duracao.Observe(time.Since(inicio).Seconds())
	}()
	agora := l.cfg.Agora()
	err = errors.Join(
		l.limparVolume(ctx, conn, agora, &r),
		l.expurgarOutbox(ctx, conn, agora.Add(-l.cfg.RetencaoOutbox), &r),
	)
	return r, err
}

// liberarLock libera o lock mesmo com o ctx cancelado e devolve a conexão ao
// pool. Se não der, fecha a conexão, que leva o lock junto.
func liberarLock(ctx context.Context, conn *pgxpool.Conn) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeoutLiberarLock)
	defer cancel()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, int64(chaveLock)); err != nil {
		descartar(ctx, conn)
		return fmt.Errorf("liberar o lock do limpador: %w", err)
	}
	conn.Release()
	return nil
}

// descartar fecha a conexão em vez de devolvê-la ao pool.
func descartar(ctx context.Context, conn *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeoutLiberarLock)
	defer cancel()
	_ = conn.Hijack().Close(ctx)
}

// limparVolume parte do que está no disco, não do banco: o custo acompanha o
// volume, e não o histórico de Vídeos, que nunca é apagado. A lista vem antes
// da consulta ao banco, para um Vídeo aceito no meio da rodada não parecer
// órfão.
func (l *Limpador) limparVolume(ctx context.Context, conn *pgxpool.Conn, agora time.Time, r *Resultado) error {
	limiteOrfao := agora.Add(-l.cfg.IdadeOrfao)
	dirs, err := l.arqs.Diretorios(ctx)
	erros := []error{err}
	for lote := range slices.Chunk(dirs, loteVideos) {
		situacoes, err := consultar(ctx, conn, lote, agora)
		if err != nil {
			// Sem resposta do banco, ninguém é órfão.
			erros = append(erros, err)
			break
		}
		for _, d := range lote {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(erros, err)...)
			}
			if err := l.limparDiretorio(ctx, d, situacoes, limiteOrfao, r); err != nil {
				erros = append(erros, fmt.Errorf("vídeo %s: %w", d.ID, err))
			}
		}
	}
	n, err := l.arqs.ApagarTemporarios(ctx, limiteOrfao)
	r.Temporarios = n
	return errors.Join(append(erros, err)...)
}

// situacao de um Vídeo que interessa ao limpador. zipSemUso: num
// Vídeo terminal, o contrário de videos.Video.ZipDisponivel. Inclui o zip de um
// Vídeo que falhou depois de publicá-lo, que não tem prazo para vencer.
type situacao struct {
	id        uuid.UUID
	terminal  bool
	zipSemUso bool
}

// consultar busca a situação dos Vídeos dos diretórios; diretório sem Vídeo
// fica fora do mapa.
func consultar(ctx context.Context, conn *pgxpool.Conn, dirs []armazenamento.Diretorio, agora time.Time) (map[uuid.UUID]situacao, error) {
	ids := make([]uuid.UUID, len(dirs))
	for i, d := range dirs {
		ids[i] = d.ID
	}
	rows, err := conn.Query(ctx, `SELECT id, status IN ($2, $3), status = $3 OR coalesce(zip_expira_em <= $4, false)
		FROM videos WHERE id = ANY($1)`, ids, videos.StatusConcluido, videos.StatusFalhou, agora)
	if err != nil {
		return nil, fmt.Errorf("buscar vídeos do volume: %w", err)
	}
	lista, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (situacao, error) {
		var s situacao
		err := row.Scan(&s.id, &s.terminal, &s.zipSemUso)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("buscar vídeos do volume: %w", err)
	}
	mapa := make(map[uuid.UUID]situacao, len(lista))
	for _, s := range lista {
		mapa[s.id] = s
	}
	return mapa, nil
}

func (l *Limpador) limparDiretorio(ctx context.Context, d armazenamento.Diretorio, situacoes map[uuid.UUID]situacao, limiteOrfao time.Time, r *Resultado) error {
	s, temVideo := situacoes[d.ID]
	if !temVideo {
		// Queda entre gravar e confirmar (ADR-0003) ou envio em andamento: só
		// a idade separa um do outro.
		if d.ModificadoEm.Before(limiteOrfao) {
			return contar(&r.Orfaos, l.arqs.Apagar(ctx, d.ID))
		}
		return nil
	}
	if !s.terminal {
		return nil
	}
	var erros []error
	if d.TemOriginal {
		erros = append(erros, contar(&r.Originais, l.arqs.ApagarOriginal(ctx, d.ID)))
	}
	if d.TemZip && s.zipSemUso {
		erros = append(erros, contar(&r.Zips, l.arqs.ApagarZip(ctx, d.ID)))
	}
	// Vídeo terminal sem arquivos: o diretório só atrasaria as próximas rodadas.
	erros = append(erros, l.arqs.ApagarSeVazio(ctx, d.ID))
	return errors.Join(erros...)
}

// contar soma um em n se a operação deu certo, e devolve o erro dela.
func contar(n *int, err error) error {
	if err == nil {
		*n++
	}
	return err
}

// expurgarOutbox apaga em lotes as linhas publicadas antes de limite. A linha
// pendente tem publicado_em nulo, que nunca passa na comparação.
func (l *Limpador) expurgarOutbox(ctx context.Context, conn *pgxpool.Conn, limite time.Time, r *Resultado) error {
	for {
		tag, err := conn.Exec(ctx, `DELETE FROM outbox WHERE id IN
			(SELECT id FROM outbox WHERE publicado_em < $1 ORDER BY id LIMIT $2)`, limite, l.cfg.LoteOutbox)
		if err != nil {
			return fmt.Errorf("expurgar outbox: %w", err)
		}
		r.Outbox += int(tag.RowsAffected())
		if tag.RowsAffected() < int64(l.cfg.LoteOutbox) {
			return nil
		}
	}
}
