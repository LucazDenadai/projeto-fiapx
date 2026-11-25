// Package armazenamento guarda vídeos e zips no volume compartilhado (ADR-0005).
package armazenamento

import (
	"context"
	"fmt"
	"os"
)

// prefixoReadyz é o do temporário da checagem, na raiz do volume.
const prefixoReadyz = ".readyz-"

// Verificacao devolve a checagem de readiness do volume: o diretório existe e
// aceita escrita (cria e apaga um arquivo temporário).
func Verificacao(dir string) func(context.Context) error {
	return func(context.Context) error {
		f, err := os.CreateTemp(dir, prefixoReadyz+"*")
		if err != nil {
			return fmt.Errorf("gravar no volume %s: %w", dir, err)
		}
		nome := f.Name()
		if err := f.Close(); err != nil {
			_ = os.Remove(nome)
			return fmt.Errorf("fechar temporário no volume: %w", err)
		}
		if err := os.Remove(nome); err != nil {
			return fmt.Errorf("apagar temporário no volume: %w", err)
		}
		return nil
	}
}
