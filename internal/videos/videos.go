// Package videos é o lado da API do ciclo de vida do Vídeo: recebe o envio,
// cria o Vídeo com a linha da outbox e lista os Vídeos do Usuário (ADR-0006,
// ADR-0014).
package videos

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Erros com mensagem pública.
var (
	ErrNaoEhVideo     = errors.New("o arquivo não é um vídeo em formato aceito")
	ErrNaoEncontrado  = errors.New("vídeo não encontrado")
	ErrCursorInvalido = errors.New("cursor inválido")
	ErrCotaExcedida   = errors.New("você já tem o máximo de vídeos em processamento; envie outro quando um terminar")
)

// Status do Vídeo (ADR-0014).
const (
	StatusRecebido    = "recebido"
	StatusProcessando = "processando"
	StatusConcluido   = "concluido"
	StatusFalhou      = "falhou"
)

// Video é a visão de um Vídeo para o seu dono.
type Video struct {
	ID           uuid.UUID
	NomeOriginal string
	Status       string
	Motivo       string // só em falhou
	CriadoEm     time.Time
	AtualizadoEm time.Time
	ZipExpiraEm  *time.Time
}

// Armazenamento é o que o envio precisa do volume (ADR-0005).
type Armazenamento interface {
	GravarOriginal(ctx context.Context, id uuid.UUID, conteudo io.Reader) error
	Apagar(ctx context.Context, id uuid.UUID) error
}

// Servico recebe envios e consulta Vídeos.
type Servico struct {
	repo repositorio
	arqs Armazenamento
	cota int
	log  *slog.Logger
}

// NovoServico monta o serviço sobre o Postgres e o volume. cota é o máximo de
// Vídeos não terminais por Usuário (ADR-0011).
func NovoServico(pool *pgxpool.Pool, arqs Armazenamento, cota int, log *slog.Logger) *Servico {
	return &Servico{repo: repositorio{pool: pool}, arqs: arqs, cota: cota, log: log}
}

// ConferirCota recusa com ErrCotaExcedida, antes de gravar o arquivo, o envio
// de quem já está na cota. É a checagem barata; a que vale é a de Aceitar.
func (s *Servico) ConferirCota(ctx context.Context, usuarioID uuid.UUID) error {
	n, err := s.repo.emAndamento(ctx, s.repo.pool, usuarioID)
	if err != nil {
		return fmt.Errorf("conferir cota: %w", err)
	}
	if n >= s.cota {
		return ErrCotaExcedida
	}
	return nil
}

// Recebido é um original já gravado no volume, ainda sem Vídeo no banco.
// Termina em Aceitar ou Descartar.
type Recebido struct {
	id   uuid.UUID
	nome string
}

// Receber confere os magic bytes e grava o conteúdo como original de um Vídeo
// novo. Erros esperados: ErrNaoEhVideo e os de leitura do corpo (por exemplo
// *http.MaxBytesError), sempre sem nada gravado.
func (s *Servico) Receber(ctx context.Context, nome string, conteudo io.Reader) (Recebido, error) {
	leitor := bufio.NewReaderSize(conteudo, tamCabecalho)
	cabecalho, err := leitor.Peek(tamCabecalho)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return Recebido{}, fmt.Errorf("ler início do arquivo: %w", err)
	}
	if !pareceVideo(cabecalho) {
		return Recebido{}, ErrNaoEhVideo
	}
	r := Recebido{id: uuid.New(), nome: sanitizarNome(nome)}
	if err := s.arqs.GravarOriginal(ctx, r.id, leitor); err != nil {
		return Recebido{}, fmt.Errorf("receber vídeo: %w", err)
	}
	return r, nil
}

// Descartar apaga o original de um envio que não será aceito.
func (s *Servico) Descartar(ctx context.Context, r Recebido) {
	if err := s.arqs.Apagar(context.WithoutCancel(ctx), r.id); err != nil {
		s.log.ErrorContext(ctx, "descartar envio", "video_id", r.id, "erro", err)
	}
}

// Aceitar cria o Vídeo recebido e a linha da outbox na mesma transação
// (ADR-0003), conferindo de novo a cota com a linha do Usuário travada, para
// que envios simultâneos não passem juntos. Se falhar, inclusive com
// ErrCotaExcedida, o original é descartado.
func (s *Servico) Aceitar(ctx context.Context, usuarioID uuid.UUID, r Recebido) (Video, error) {
	correlationID := uuid.New()
	v, err := s.repo.criar(ctx, usuarioID, r.id, r.nome, correlationID, s.cota)
	if err != nil {
		s.Descartar(ctx, r)
		return Video{}, fmt.Errorf("aceitar vídeo: %w", err)
	}
	s.log.InfoContext(ctx, "vídeo aceito", "video_id", v.ID, "correlation_id", correlationID)
	return v, nil
}

// Buscar devolve o Vídeo se ele for do Usuário; senão ErrNaoEncontrado.
func (s *Servico) Buscar(ctx context.Context, usuarioID, id uuid.UUID) (Video, error) {
	return s.repo.buscar(ctx, usuarioID, id)
}

// Pagina é um trecho da listagem e o cursor do próximo, vazio no fim.
type Pagina struct {
	Videos  []Video
	Proximo string
}

// Listar devolve até limite Vídeos do Usuário, do mais recente para o mais
// antigo, a partir do cursor (vazio = do começo).
func (s *Servico) Listar(ctx context.Context, usuarioID uuid.UUID, cursor string, limite int) (Pagina, error) {
	var depois *posicao
	if cursor != "" {
		p, err := decodificarCursor(cursor)
		if err != nil {
			return Pagina{}, ErrCursorInvalido
		}
		depois = &p
	}
	lista, err := s.repo.listar(ctx, usuarioID, depois, limite+1)
	if err != nil {
		return Pagina{}, err
	}
	pag := Pagina{Videos: lista}
	if len(lista) > limite {
		pag.Videos = lista[:limite]
		ultimo := pag.Videos[limite-1]
		pag.Proximo = codificarCursor(posicao{criadoEm: ultimo.CriadoEm, id: ultimo.ID})
	}
	return pag, nil
}

// ZipDisponivel diz se o Zip de frames pode ser baixado agora.
func (v Video) ZipDisponivel(agora time.Time) bool {
	return v.Status == StatusConcluido && v.ZipExpiraEm != nil && agora.Before(*v.ZipExpiraEm)
}

// TextoMotivo é a explicação pública de cada Motivo da falha (ADR-0004).
var TextoMotivo = map[string]string{
	"video_invalido":    "o arquivo não pôde ser lido como vídeo",
	"video_muito_longo": "o vídeo passa da duração máxima",
	"sem_frames":        "não foi possível extrair nenhum frame do vídeo",
	"tempo_excedido":    "o processamento passou do tempo máximo",
	"erro_interno":      "não conseguimos processar, tente novamente",
}
