// Package processamento é o lado do Worker do ciclo de vida do Vídeo: consome
// o pedido de Processamento, extrai os frames, publica o Zip de frames e marca
// o Vídeo concluído, ou registra a Falha definitiva quando o erro é do próprio
// vídeo (ADR-0002, ADR-0004, ADR-0005, ADR-0011, ADR-0014, ADR-0015).
package processamento

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucazDenadai/projeto-fiapx/internal/extracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/motivo"
	"github.com/LucazDenadai/projeto-fiapx/internal/retentativa"
)

// RetencaoZipPadrao é por quanto tempo o Zip de frames fica disponível depois
// de concluído (ADR-0015).
const RetencaoZipPadrao = 7 * 24 * time.Hour

// DuracaoMaximaPadrao é a maior duração de Vídeo que o Worker extrai: até 600
// frames (ADR-0011).
const DuracaoMaximaPadrao = 10 * time.Minute

// Extrator é o que o Processamento precisa do ffprobe e do ffmpeg.
type Extrator interface {
	Sondar(ctx context.Context, arquivo string) (extracao.Sonda, error)
	ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error)
}

// Armazenamento é o que o Processamento precisa do volume (ADR-0005).
type Armazenamento interface {
	Original(id uuid.UUID) string
	PublicarZip(ctx context.Context, id uuid.UUID, escrever func(io.Writer) error) error
	ApagarOriginal(ctx context.Context, id uuid.UUID) error
}

// Config do Processador.
type Config struct {
	// DirTrabalho guarda os frames de cada Processamento, no disco efêmero do
	// container. É só do Worker: LimparTrabalho apaga tudo o que houver nele.
	DirTrabalho string
	RetencaoZip time.Duration // padrão RetencaoZipPadrao
	// TimeoutExtracao é o prazo do ffprobe e do ffmpeg somados, por
	// Processamento (ADR-0011); padrão extracao.TimeoutPadrao.
	TimeoutExtracao time.Duration
	// DuracaoMaxima acima da qual o Vídeo falha com video_muito_longo, sem
	// extrair (ADR-0011); padrão DuracaoMaximaPadrao.
	DuracaoMaxima time.Duration
	// Retry rege a nova tentativa de cada operação de infraestrutura
	// (Postgres, volume); zero usa os padrões do pacote retentativa. Esgotado
	// o orçamento, a mensagem volta à fila. Somado ao timeout do ffmpeg, tem
	// de ficar abaixo do consumer_timeout do RabbitMQ (ADR-0004).
	Retry retentativa.Politica
}

// Processador trata as mensagens da fila processamento.
type Processador struct {
	repo repositorio
	arqs Armazenamento
	ext  Extrator
	cfg  Config
	log  *slog.Logger
}

// NovoProcessador monta o Processador.
func NovoProcessador(pool *pgxpool.Pool, arqs Armazenamento, ext Extrator, cfg Config, log *slog.Logger) *Processador {
	if cfg.RetencaoZip <= 0 {
		cfg.RetencaoZip = RetencaoZipPadrao
	}
	if cfg.TimeoutExtracao <= 0 {
		cfg.TimeoutExtracao = extracao.TimeoutPadrao
	}
	if cfg.DuracaoMaxima <= 0 {
		cfg.DuracaoMaxima = DuracaoMaximaPadrao
	}
	cfg.Retry = cfg.Retry.ComPadroes()
	return &Processador{repo: repositorio{pool: pool}, arqs: arqs, ext: ext, cfg: cfg, log: log}
}

// LimparTrabalho apaga as sobras de Processamentos interrompidos por uma queda
// (frame não é estado: a reentrega refaz do zero). Chame na subida, antes de
// consumir.
func (p *Processador) LimparTrabalho(ctx context.Context) error {
	if err := os.MkdirAll(p.cfg.DirTrabalho, 0o750); err != nil {
		return fmt.Errorf("criar diretório de trabalho: %w", err)
	}
	entradas, err := os.ReadDir(p.cfg.DirTrabalho)
	if err != nil {
		return fmt.Errorf("listar diretório de trabalho: %w", err)
	}
	for _, e := range entradas {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(p.cfg.DirTrabalho, e.Name())); err != nil {
			return fmt.Errorf("limpar diretório de trabalho: %w", err)
		}
	}
	return nil
}

// mensagem é o pedido de Processamento que a API grava na outbox.
type mensagem struct {
	VideoID       string `json:"video_id"`
	CorrelationID string `json:"correlation_id"`
}

// Processar é o mensageria.Tratador da fila processamento. Confirma (ack)
// quando o Vídeo termina concluído, quando termina falhou por erro permanente,
// quando ele já era terminal (mensagem duplicada) e quando a mensagem não
// aponta para Vídeo nenhum. Qualquer outro erro conta como transitório
// (ADR-0004): devolve a mensagem para a fila, e a reentrega refaz tudo do zero.
func (p *Processador) Processar(ctx context.Context, corpo []byte) mensageria.Resultado {
	id, correlationID, log, ok := p.lerMensagem(ctx, corpo)
	if !ok {
		return mensageria.Confirmar
	}

	err := retentativa.TentarSo(ctx, p.cfg.Retry, log, "iniciar", transitorio, func() error { return p.repo.iniciar(ctx, id) })
	switch {
	case errors.Is(err, errNaoEncontrado):
		log.ErrorContext(ctx, "vídeo inexistente; mensagem descartada")
		return mensageria.Confirmar
	case errors.Is(err, errTerminal):
		log.InfoContext(ctx, "vídeo já terminal; mensagem confirmada sem reprocessar")
		return mensageria.Confirmar
	case err != nil:
		log.WarnContext(ctx, "não deu para iniciar o processamento; mensagem devolvida", "erro", err)
		return mensageria.Devolver
	}

	inicio := time.Now()
	err = p.processar(ctx, id, correlationID, log)
	if perm, ok := errors.AsType[*falhaPermanente](err); ok {
		return p.falhar(ctx, id, correlationID, perm, log)
	}
	if err != nil {
		log.WarnContext(ctx, "processamento interrompido; mensagem devolvida", "erro", err)
		return mensageria.Devolver
	}
	log.InfoContext(ctx, "vídeo concluído", "duracao", time.Since(inicio).Round(time.Millisecond).String())
	return mensageria.Confirmar
}

// ProcessarDLQ é o mensageria.Tratador da processamento.dlq: a mensagem chega
// aqui quando esgotou as entregas na fila processamento, inclusive por um
// Vídeo que derruba o Worker. Registra a Falha definitiva com erro_interno e
// o evento falhou; se o Vídeo já é terminal (outra cópia terminou), só
// confirma, sem evento (ADR-0004). A DLQ não tem delivery-limit: enquanto o
// registro não sai, a mensagem volta para ela.
func (p *Processador) ProcessarDLQ(ctx context.Context, corpo []byte) mensageria.Resultado {
	id, correlationID, log, ok := p.lerMensagem(ctx, corpo)
	if !ok {
		return mensageria.Confirmar
	}
	falhou, err := retentativa.Tentar(ctx, p.cfg.Retry, log, "registrar falha", transitorio, func() (bool, error) {
		return p.repo.falhar(ctx, id, correlationID, motivo.ErroInterno)
	})
	if err != nil {
		log.WarnContext(ctx, "dlq: não deu para registrar a falha; mensagem devolvida", "erro", err)
		return mensageria.Devolver
	}
	if !falhou {
		log.InfoContext(ctx, "dlq: vídeo já terminal ou inexistente; confirmada sem evento")
		return mensageria.Confirmar
	}
	log.ErrorContext(ctx, "falha definitiva: tentativas esgotadas", "motivo", motivo.ErroInterno)
	p.apagarOriginal(ctx, id, log)
	return mensageria.Confirmar
}

// lerMensagem decodifica o pedido de Processamento. Mensagem ilegível ou sem
// video_id válido não tem como dar certo: ok false, e quem chama descarta.
func (p *Processador) lerMensagem(ctx context.Context, corpo []byte) (id uuid.UUID, correlationID string, log *slog.Logger, ok bool) {
	var msg mensagem
	if err := json.Unmarshal(corpo, &msg); err != nil {
		p.log.ErrorContext(ctx, "mensagem de processamento ilegível; descartada", "erro", err)
		return id, "", nil, false
	}
	id, err := uuid.Parse(msg.VideoID)
	if err != nil {
		p.log.ErrorContext(ctx, "mensagem de processamento sem video_id válido; descartada", "correlation_id", msg.CorrelationID)
		return id, "", nil, false
	}
	return id, msg.CorrelationID, p.log.With("video_id", id, "correlation_id", msg.CorrelationID), true
}

// transitorio diz se vale tentar de novo: não com as respostas definitivas
// do repositório.
func transitorio(err error) bool {
	return !errors.Is(err, errNaoEncontrado) && !errors.Is(err, errTerminal)
}

// falhaPermanente é um erro permanente do ADR-0004, do próprio vídeo: tentar
// de novo dá o mesmo resultado, então ele vira Falha definitiva na hora.
// motivo é o código público; err, o detalhe que só vai ao log.
type falhaPermanente struct {
	motivo string
	err    error
}

func (f *falhaPermanente) Error() string { return f.motivo + ": " + f.err.Error() }
func (f *falhaPermanente) Unwrap() error { return f.err }

// falhar registra a Falha definitiva e confirma a mensagem, sem passar pela
// DLQ (ADR-0004). Se a transação não sair, a mensagem volta à fila e a
// reentrega chega à mesma conclusão.
func (p *Processador) falhar(ctx context.Context, id uuid.UUID, correlationID string, perm *falhaPermanente, log *slog.Logger) mensageria.Resultado {
	falhou, err := retentativa.Tentar(ctx, p.cfg.Retry, log, "registrar falha", transitorio, func() (bool, error) {
		return p.repo.falhar(ctx, id, correlationID, perm.motivo)
	})
	if err != nil {
		log.WarnContext(ctx, "não deu para registrar a falha; mensagem devolvida", "motivo", perm.motivo, "erro", err)
		return mensageria.Devolver
	}
	if !falhou {
		log.InfoContext(ctx, "outra entrega já terminou o vídeo", "motivo", perm.motivo, "erro", perm.err)
		return mensageria.Confirmar
	}
	// O detalhe, inclusive a saída do ffmpeg, fica só aqui: nunca no evento
	// nem na listagem.
	log.WarnContext(ctx, "falha definitiva", "motivo", perm.motivo, "erro", perm.err)
	p.apagarOriginal(ctx, id, log)
	return mensageria.Confirmar
}

// apagarOriginal remove o original de um Vídeo já terminal: sem
// reprocessamento, ele não serve mais. Se falhar, o limpador recolhe depois
// (ADR-0015).
func (p *Processador) apagarOriginal(ctx context.Context, id uuid.UUID, log *slog.Logger) {
	if err := p.arqs.ApagarOriginal(ctx, id); err != nil {
		log.ErrorContext(ctx, "apagar original", "erro", err)
	}
}

// processar extrai os frames num temporário próprio, publica o zip e marca o
// Vídeo concluído. O temporário some ao final, com sucesso ou erro.
func (p *Processador) processar(ctx context.Context, id uuid.UUID, correlationID string, log *slog.Logger) error {
	dir, err := os.MkdirTemp(p.cfg.DirTrabalho, id.String()+"-*")
	if err != nil {
		return fmt.Errorf("criar temporário: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.ErrorContext(ctx, "apagar temporário", "erro", err)
		}
	}()

	// O original vem do volume: se ele piscar, tenta de novo aqui, antes do
	// ffprobe, em vez de gastar as entregas (ADR-0004). O ffmpeg em si não é
	// repetido no processo, para não estourar o consumer_timeout.
	original := p.arqs.Original(id)
	if err := retentativa.TentarSo(ctx, p.cfg.Retry, log, "ler original", transitorio, func() error {
		_, err := os.Stat(original)
		return err
	}); err != nil {
		return fmt.Errorf("original: %w", err)
	}
	frames, err := p.extrair(ctx, original, dir)
	if err != nil {
		return err
	}
	err = retentativa.TentarSo(ctx, p.cfg.Retry, log, "publicar zip", transitorio, func() error {
		return p.arqs.PublicarZip(ctx, id, func(w io.Writer) error { return escreverZip(ctx, w, frames) })
	})
	if err != nil {
		return err
	}
	concluiu, err := retentativa.Tentar(ctx, p.cfg.Retry, log, "concluir", transitorio, func() (bool, error) {
		return p.repo.concluir(ctx, id, correlationID, p.cfg.RetencaoZip)
	})
	if err != nil {
		return err
	}
	if !concluiu {
		log.InfoContext(ctx, "outra entrega já terminou o vídeo")
	}
	p.apagarOriginal(ctx, id, log)
	return nil
}

// extrair sonda o original e grava os frames em dir, tudo dentro de um único
// prazo: o timeout vale para o Processamento, não para cada execução. Erro do
// próprio vídeo volta como *falhaPermanente.
func (p *Processador) extrair(ctx context.Context, original, dir string) ([]string, error) {
	ctxExtracao, cancel := context.WithTimeout(ctx, p.cfg.TimeoutExtracao)
	defer cancel()
	sonda, err := p.ext.Sondar(ctxExtracao, original)
	if err != nil {
		return nil, classificar(ctx, err)
	}
	if sonda.Duracao > p.cfg.DuracaoMaxima {
		return nil, muitoLongo(fmt.Sprintf("duração %v", sonda.Duracao), p.cfg.DuracaoMaxima)
	}
	frames, err := p.ext.ExtrairFrames(ctxExtracao, original, dir)
	if err != nil {
		return nil, classificar(ctx, err)
	}
	if len(frames) == 0 {
		return nil, &falhaPermanente{motivo.SemFrames, errors.New("o ffmpeg não extraiu nenhum frame")}
	}
	// Quando o contêiner não informa a duração, a sonda não barra: com 1
	// frame por segundo, a contagem confere (um a mais pelo arredondamento).
	if maximo := int(p.cfg.DuracaoMaxima/time.Second) + 1; len(frames) > maximo {
		return nil, muitoLongo(fmt.Sprintf("%d frames", len(frames)), p.cfg.DuracaoMaxima)
	}
	return frames, nil
}

func muitoLongo(medida string, maxima time.Duration) *falhaPermanente {
	return &falhaPermanente{motivo.VideoMuitoLongo, fmt.Errorf("%s acima da duração máxima %v", medida, maxima)}
}

// classificar separa os erros permanentes do ffprobe e do ffmpeg (ADR-0004).
// O prazo estourado só é tempo_excedido se ctx, o do Processamento inteiro,
// ainda vale: o Worker desligando não é culpa do vídeo. O resto é transitório.
func classificar(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return err
	case errors.Is(err, context.DeadlineExceeded):
		return &falhaPermanente{motivo.TempoExcedido, err}
	case errors.Is(err, extracao.ErrVideoInvalido):
		return &falhaPermanente{motivo.VideoInvalido, err}
	default:
		return err
	}
}

// escreverZip empacota os frames sem compressão: PNG quase não comprime, e
// deflate só gastaria CPU (ADR-0011).
func escreverZip(ctx context.Context, w io.Writer, frames []string) error {
	zw := zip.NewWriter(w)
	agora := time.Now()
	for _, f := range frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		destino, err := zw.CreateHeader(&zip.FileHeader{Name: filepath.Base(f), Method: zip.Store, Modified: agora})
		if err != nil {
			return fmt.Errorf("zip: %w", err)
		}
		if err := copiar(destino, f); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("zip: %w", err)
	}
	return nil
}

func copiar(w io.Writer, caminho string) error {
	f, err := os.Open(caminho) //nolint:gosec // frame que o ffmpeg gravou no temporário deste Processamento
	if err != nil {
		return fmt.Errorf("abrir frame: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("copiar frame: %w", err)
	}
	return nil
}
