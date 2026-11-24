// Package processamento é o lado do Worker do ciclo de vida do Vídeo: consome
// o pedido de Processamento, extrai os frames, publica o Zip de frames e marca
// o Vídeo concluído (ADR-0002, ADR-0005, ADR-0014, ADR-0015).
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
)

// RetencaoZipPadrao é por quanto tempo o Zip de frames fica disponível depois
// de concluído (ADR-0015).
const RetencaoZipPadrao = 7 * 24 * time.Hour

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
	return &Processador{repo: repositorio{pool: pool}, arqs: arqs, ext: ext, cfg: cfg, log: log}
}

// LimparTrabalho apaga as sobras de Processamentos interrompidos por uma queda
// (frame não é estado: a reentrega refaz do zero). Chame na subida, antes de
// consumir.
func (p *Processador) LimparTrabalho() error {
	if err := os.MkdirAll(p.cfg.DirTrabalho, 0o750); err != nil {
		return fmt.Errorf("criar diretório de trabalho: %w", err)
	}
	entradas, err := os.ReadDir(p.cfg.DirTrabalho)
	if err != nil {
		return fmt.Errorf("listar diretório de trabalho: %w", err)
	}
	for _, e := range entradas {
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
// quando o Vídeo termina concluído, quando ele já era terminal (mensagem
// duplicada) e quando a mensagem não aponta para Vídeo nenhum. Qualquer outro
// erro devolve a mensagem para a fila, e a reentrega refaz tudo do zero.
func (p *Processador) Processar(ctx context.Context, corpo []byte) mensageria.Resultado {
	var msg mensagem
	if err := json.Unmarshal(corpo, &msg); err != nil {
		p.log.ErrorContext(ctx, "mensagem de processamento ilegível; descartada", "erro", err)
		return mensageria.Confirmar
	}
	id, err := uuid.Parse(msg.VideoID)
	if err != nil {
		p.log.ErrorContext(ctx, "mensagem de processamento sem video_id válido; descartada", "correlation_id", msg.CorrelationID)
		return mensageria.Confirmar
	}
	log := p.log.With("video_id", id, "correlation_id", msg.CorrelationID)

	err = p.repo.iniciar(ctx, id)
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
	if err := p.processar(ctx, id, msg.CorrelationID, log); err != nil {
		log.WarnContext(ctx, "processamento interrompido; mensagem devolvida", "erro", err)
		return mensageria.Devolver
	}
	log.InfoContext(ctx, "vídeo concluído", "duracao", time.Since(inicio).Round(time.Millisecond).String())
	return mensageria.Confirmar
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

	original := p.arqs.Original(id)
	if _, err := p.ext.Sondar(ctx, original); err != nil {
		return err
	}
	frames, err := p.ext.ExtrairFrames(ctx, original, dir)
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return errors.New("nenhum frame extraído")
	}
	if err := p.arqs.PublicarZip(ctx, id, func(w io.Writer) error { return escreverZip(ctx, w, frames) }); err != nil {
		return err
	}
	concluiu, err := p.repo.concluir(ctx, id, correlationID, p.cfg.RetencaoZip)
	if err != nil {
		return err
	}
	if !concluiu {
		log.InfoContext(ctx, "outra entrega já terminou o vídeo")
	}
	// O Vídeo já é terminal: sem reprocessamento, o original não serve mais.
	// Se a remoção falhar, o limpador recolhe depois (ADR-0015).
	if err := p.arqs.ApagarOriginal(ctx, id); err != nil {
		log.ErrorContext(ctx, "apagar original", "erro", err)
	}
	return nil
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
