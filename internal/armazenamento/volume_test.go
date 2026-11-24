package armazenamento_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
)

func gravarOriginal(t *testing.T, v *armazenamento.Volume, id uuid.UUID) {
	t.Helper()
	if err := v.GravarOriginal(t.Context(), id, strings.NewReader("vídeo")); err != nil {
		t.Fatalf("gravar original: %v", err)
	}
}

func arquivos(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listar %s: %v", dir, err)
	}
	var nomes []string
	for _, e := range ents {
		nomes = append(nomes, e.Name())
	}
	return nomes
}

func TestOriginal_CaminhoDentroDoDiretorioDoVideo(t *testing.T) {
	raiz := t.TempDir()
	v := armazenamento.NovoVolume(raiz)
	id := uuid.New()
	gravarOriginal(t, v, id)

	conteudo, err := os.ReadFile(v.Original(id))
	if err != nil || string(conteudo) != "vídeo" {
		t.Fatalf("ler original em %s: %q, %v", v.Original(id), conteudo, err)
	}
	if want := filepath.Join(raiz, "videos", id.String(), "original"); v.Original(id) != want {
		t.Errorf("Original = %s, quero %s", v.Original(id), want)
	}
}

func TestPublicarZip_GravaFramesZipNoDiretorioDoVideo(t *testing.T) {
	raiz := t.TempDir()
	v := armazenamento.NovoVolume(raiz)
	id := uuid.New()
	gravarOriginal(t, v, id)

	err := v.PublicarZip(t.Context(), id, func(w io.Writer) error {
		_, err := io.WriteString(w, "zip")
		return err
	})
	if err != nil {
		t.Fatalf("publicar: %v", err)
	}
	dir := filepath.Join(raiz, "videos", id.String())
	conteudo, err := os.ReadFile(filepath.Join(dir, "frames.zip")) //nolint:gosec // caminho do próprio teste
	if err != nil || string(conteudo) != "zip" {
		t.Fatalf("frames.zip = %q, %v", conteudo, err)
	}
	if nomes := arquivos(t, dir); len(nomes) != 2 {
		t.Errorf("arquivos no diretório = %v, quero só original e frames.zip", nomes)
	}
}

func TestPublicarZip_FalhaNoMeioNaoDeixaZipNemTemporario(t *testing.T) {
	raiz := t.TempDir()
	v := armazenamento.NovoVolume(raiz)
	id := uuid.New()
	gravarOriginal(t, v, id)

	falha := errors.New("disco cheio")
	err := v.PublicarZip(t.Context(), id, func(w io.Writer) error {
		_, _ = io.WriteString(w, "metade")
		return falha
	})
	if !errors.Is(err, falha) {
		t.Fatalf("erro = %v, quero o da escrita", err)
	}
	dir := filepath.Join(raiz, "videos", id.String())
	if nomes := arquivos(t, dir); len(nomes) != 1 || nomes[0] != "original" {
		t.Errorf("arquivos no diretório = %v, quero só o original", nomes)
	}
}

func TestPublicarZip_ReentregaSobrescreve(t *testing.T) {
	v := armazenamento.NovoVolume(t.TempDir())
	id := uuid.New()
	gravarOriginal(t, v, id)
	for _, c := range []string{"primeiro", "segundo"} {
		if err := v.PublicarZip(t.Context(), id, func(w io.Writer) error {
			_, err := io.WriteString(w, c)
			return err
		}); err != nil {
			t.Fatalf("publicar %s: %v", c, err)
		}
	}
	conteudo, _ := os.ReadFile(filepath.Join(filepath.Dir(v.Original(id)), "frames.zip"))
	if string(conteudo) != "segundo" {
		t.Errorf("frames.zip = %q, quero o da última publicação", conteudo)
	}
}

func TestApagarOriginal_MantemOZip(t *testing.T) {
	v := armazenamento.NovoVolume(t.TempDir())
	id := uuid.New()
	gravarOriginal(t, v, id)
	if err := v.PublicarZip(t.Context(), id, func(w io.Writer) error { return nil }); err != nil {
		t.Fatal(err)
	}

	if err := v.ApagarOriginal(t.Context(), id); err != nil {
		t.Fatalf("apagar original: %v", err)
	}
	if _, err := os.Stat(v.Original(id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("original ainda existe: %v", err)
	}
	if nomes := arquivos(t, filepath.Dir(v.Original(id))); len(nomes) != 1 || nomes[0] != "frames.zip" {
		t.Errorf("arquivos = %v, quero só frames.zip", nomes)
	}
	// Apagar de novo (reentrega) não é erro.
	if err := v.ApagarOriginal(t.Context(), id); err != nil {
		t.Errorf("apagar original inexistente: %v", err)
	}
}
