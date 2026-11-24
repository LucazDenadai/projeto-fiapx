package armazenamento

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// Volume guarda os arquivos de cada Vídeo em <dir>/videos/<video_id>/, com
// caminhos gerados a partir do id, nunca do nome enviado (ADR-0005).
type Volume struct {
	dir string
}

// NovoVolume usa dir como raiz (o /data do Compose).
func NovoVolume(dir string) *Volume {
	return &Volume{dir: dir}
}

func (v *Volume) dirVideo(id uuid.UUID) string {
	// uuid.UUID só formata hexadecimal e hífens: não há como escapar do dir.
	return filepath.Join(v.dir, "videos", id.String())
}

// Nomes dos arquivos de um Vídeo no diretório dele.
const (
	nomeOriginal = "original"
	nomeZip      = "frames.zip"
)

// Original devolve o caminho do original do Vídeo.
func (v *Volume) Original(id uuid.UUID) string {
	return filepath.Join(v.dirVideo(id), nomeOriginal)
}

// GravarOriginal copia conteudo para videos/<id>/original de forma atômica.
// Se a cópia falhar, nada fica no volume.
func (v *Volume) GravarOriginal(ctx context.Context, id uuid.UUID, conteudo io.Reader) error {
	dir := v.dirVideo(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("criar diretório do vídeo: %w", err)
	}
	err := gravarAtomico(dir, nomeOriginal, func(w io.Writer) error {
		_, err := io.Copy(w, leitorComContexto{ctx, conteudo})
		return err
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("gravar original: %w", err)
	}
	return nil
}

// PublicarZip grava o Zip de frames do Vídeo em videos/<id>/frames.zip com o
// que escrever produzir, de forma atômica: um leitor nunca vê um zip pela
// metade, e uma nova publicação (reentrega) substitui a anterior inteira.
func (v *Volume) PublicarZip(ctx context.Context, id uuid.UUID, escrever func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := gravarAtomico(v.dirVideo(id), nomeZip, escrever); err != nil {
		return fmt.Errorf("publicar zip: %w", err)
	}
	return nil
}

// AbrirZip abre o Zip de frames do Vídeo para leitura. Zip ausente dá um erro
// que embrulha os.ErrNotExist.
func (v *Volume) AbrirZip(_ context.Context, id uuid.UUID) (*os.File, error) {
	f, err := os.Open(filepath.Join(v.dirVideo(id), nomeZip))
	if err != nil {
		return nil, fmt.Errorf("abrir zip: %w", err)
	}
	return f, nil
}

// ApagarOriginal remove o original do Vídeo; se ele já não existir, não é erro.
func (v *Volume) ApagarOriginal(_ context.Context, id uuid.UUID) error {
	if err := os.Remove(v.Original(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("apagar original: %w", err)
	}
	return nil
}

// gravarAtomico grava num temporário no próprio dir e publica com rename
// (ADR-0005). Se escrever falhar, o temporário é removido e o destino fica
// como estava.
func gravarAtomico(dir, nome string, escrever func(io.Writer) error) (err error) {
	tmp, err := os.CreateTemp(dir, "."+nome+"-*")
	if err != nil {
		return fmt.Errorf("criar temporário: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := escrever(tmp); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sincronizar: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fechar: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, nome)); err != nil {
		return fmt.Errorf("renomear: %w", err)
	}
	return nil
}

// Apagar remove tudo o que o Vídeo tem no volume.
func (v *Volume) Apagar(_ context.Context, id uuid.UUID) error {
	if err := os.RemoveAll(v.dirVideo(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("apagar arquivos do vídeo: %w", err)
	}
	return nil
}

// leitorComContexto interrompe a cópia quando o contexto é cancelado.
type leitorComContexto struct {
	ctx context.Context
	r   io.Reader
}

func (l leitorComContexto) Read(p []byte) (int, error) {
	if err := l.ctx.Err(); err != nil {
		return 0, err
	}
	return l.r.Read(p)
}
