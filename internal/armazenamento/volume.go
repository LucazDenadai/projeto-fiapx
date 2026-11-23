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

// GravarOriginal copia conteudo para videos/<id>/original de forma atômica:
// grava num temporário no mesmo diretório e publica com rename. Se a cópia
// falhar, nada fica no volume.
func (v *Volume) GravarOriginal(ctx context.Context, id uuid.UUID, conteudo io.Reader) (err error) {
	dir := v.dirVideo(id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("criar diretório do vídeo: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	tmp, err := os.CreateTemp(dir, ".original-*")
	if err != nil {
		return fmt.Errorf("criar temporário: %w", err)
	}
	defer func() { _ = tmp.Close() }()

	if _, err := io.Copy(tmp, leitorComContexto{ctx, conteudo}); err != nil {
		return fmt.Errorf("gravar original: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sincronizar original: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fechar original: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "original")); err != nil {
		return fmt.Errorf("publicar original: %w", err)
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
