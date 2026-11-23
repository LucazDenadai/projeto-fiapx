package armazenamento_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
)

func TestVerificacao_VolumeGravavel(t *testing.T) {
	dir := t.TempDir()
	if err := armazenamento.Verificacao(dir)(context.Background()); err != nil {
		t.Fatalf("diretório gravável deveria passar: %v", err)
	}
	sobras, _ := os.ReadDir(dir)
	if len(sobras) != 0 {
		t.Errorf("a verificação deixou arquivos no volume: %v", sobras)
	}
}

func TestVerificacao_VolumeAusente(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nao-existe")
	if err := armazenamento.Verificacao(dir)(context.Background()); err == nil {
		t.Fatal("volume ausente deveria falhar")
	}
}
