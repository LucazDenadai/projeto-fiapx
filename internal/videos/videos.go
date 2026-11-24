// Package videos é o lado da API do ciclo de vida do Vídeo: recebe o envio,
// cria o Vídeo com a linha da outbox, lista os Vídeos do Usuário e entrega o
// Zip de frames (ADR-0006, ADR-0014, ADR-0015).
package videos

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"strings"
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

// Armazenamento é o que o envio e o download precisam do volume (ADR-0005).
type Armazenamento interface {
	GravarOriginal(ctx context.Context, id uuid.UUID, conteudo io.Reader) error
	Apagar(ctx context.Context, id uuid.UUID) error
	AbrirZip(ctx context.Context, id uuid.UUID) (*os.File, error)
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

// ErrZipExpirado: o Vídeo concluiu, mas o Zip de frames passou da retenção
// (ADR-0015).
var ErrZipExpirado = errors.New("o zip de frames expirou; envie o vídeo de novo")

// ErroNaoConcluido: o Vídeo ainda não tem Zip de frames, ou nunca vai ter
// (falhou). Video vai junto para a resposta mostrar o status e o Motivo da
// falha.
type ErroNaoConcluido struct {
	Video Video
}

func (e *ErroNaoConcluido) Error() string {
	if e.Video.Status == StatusFalhou {
		return "o processamento do vídeo falhou"
	}
	return "o vídeo ainda está em processamento"
}

// Zip é o Zip de frames aberto para download. Quem recebe fecha Arquivo.
type Zip struct {
	Arquivo *os.File
	// Nome é o nome de download: <nome original sem extensão>-frames.zip.
	Nome string
}

// AbrirZip abre o Zip de frames de um Vídeo do Usuário. O dono é checado
// antes de tudo: Vídeo alheio dá ErrNaoEncontrado, como o inexistente
// (ADR-0006). Depois, *ErroNaoConcluido se o Vídeo não concluiu e
// ErrZipExpirado se o zip passou da retenção ou já saiu do volume.
func (s *Servico) AbrirZip(ctx context.Context, usuarioID, id uuid.UUID, agora time.Time) (Zip, error) {
	v, err := s.repo.buscar(ctx, usuarioID, id)
	if err != nil {
		return Zip{}, err
	}
	if v.Status != StatusConcluido {
		return Zip{}, &ErroNaoConcluido{Video: v}
	}
	if !v.ZipDisponivel(agora) {
		return Zip{}, ErrZipExpirado
	}
	f, err := s.arqs.AbrirZip(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		// O limpador apagou o zip entre a consulta e a abertura.
		s.log.WarnContext(ctx, "zip ausente no volume de vídeo concluído", "video_id", id)
		return Zip{}, ErrZipExpirado
	}
	if err != nil {
		return Zip{}, fmt.Errorf("abrir zip: %w", err)
	}
	return Zip{Arquivo: f, Nome: nomeDoZip(v.NomeOriginal)}, nil
}

// nomeDoZip troca a extensão do nome original por -frames.zip.
func nomeDoZip(nomeOriginal string) string {
	base := strings.TrimSuffix(nomeOriginal, path.Ext(nomeOriginal))
	if base == "" {
		base = "video"
	}
	return base + "-frames.zip"
}

// ZipDisponivel diz se o Zip de frames pode ser baixado agora.
func (v Video) ZipDisponivel(agora time.Time) bool {
	return v.Status == StatusConcluido && v.ZipExpiraEm != nil && agora.Before(*v.ZipExpiraEm)
}

// Motivos da falha: códigos públicos, contrato da listagem e da notificação
// (ADR-0004, ADR-0011).
const (
	MotivoVideoInvalido   = "video_invalido"
	MotivoVideoMuitoLongo = "video_muito_longo"
	MotivoSemFrames       = "sem_frames"
	MotivoTempoExcedido   = "tempo_excedido"
	MotivoErroInterno     = "erro_interno"
)

// TextoMotivo é a explicação pública de cada Motivo da falha (ADR-0004).
var TextoMotivo = map[string]string{
	MotivoVideoInvalido:   "o arquivo não pôde ser lido como vídeo",
	MotivoVideoMuitoLongo: "o vídeo passa da duração máxima",
	MotivoSemFrames:       "não foi possível extrair nenhum frame do vídeo",
	MotivoTempoExcedido:   "o processamento passou do tempo máximo",
	MotivoErroInterno:     "não conseguimos processar, tente novamente",
}
