package armazenamento

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Diretorio é o que um Vídeo tem no volume, para o limpador (ADR-0015).
type Diretorio struct {
	ID          uuid.UUID
	TemOriginal bool
	TemZip      bool
	// ModificadoEm é a modificação mais recente do diretório ou de algo nele:
	// um envio em andamento não parece velho.
	ModificadoEm time.Time
}

// Diretorios lista os diretórios de Vídeo do volume. Um diretório ilegível
// fica de fora e vai no erro, junto com a lista dos outros.
func (v *Volume) Diretorios(ctx context.Context) ([]Diretorio, error) {
	ids, err := v.idsNoVolume()
	if err != nil {
		return nil, err
	}
	var lista []Diretorio
	var erros []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d, err := v.lerDiretorio(id)
		switch {
		case errors.Is(err, os.ErrNotExist): // apagado durante a listagem
		case err != nil:
			erros = append(erros, err)
		default:
			lista = append(lista, d)
		}
	}
	return lista, errors.Join(erros...)
}

// idsNoVolume devolve os ids dos diretórios de Vídeo; o que não tem nome de
// diretório de Vídeo é ignorado.
func (v *Volume) idsNoVolume() ([]uuid.UUID, error) {
	entradas, err := os.ReadDir(filepath.Join(v.dir, dirVideos))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // nenhum Vídeo enviado ainda
	}
	if err != nil {
		return nil, fmt.Errorf("listar volume: %w", err)
	}
	var ids []uuid.UUID
	for _, e := range entradas {
		// uuid.Parse aceita outras grafias; só a que o servidor gera vale.
		if id, err := uuid.Parse(e.Name()); err == nil && id.String() == e.Name() && e.IsDir() {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (v *Volume) lerDiretorio(id uuid.UUID) (Diretorio, error) {
	info, err := os.Stat(v.dirVideo(id))
	if err != nil {
		return Diretorio{}, fmt.Errorf("ler diretório do vídeo: %w", err)
	}
	d := Diretorio{ID: id, ModificadoEm: info.ModTime()}
	entradas, err := os.ReadDir(v.dirVideo(id))
	if err != nil {
		return Diretorio{}, fmt.Errorf("ler diretório do vídeo: %w", err)
	}
	for _, e := range entradas {
		switch e.Name() {
		case nomeOriginal:
			d.TemOriginal = true
		case nomeZip:
			d.TemZip = true
		}
		info, err := e.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue // temporário publicado ou apagado durante a leitura
		}
		if err != nil {
			return Diretorio{}, fmt.Errorf("ler diretório do vídeo: %w", err)
		}
		if info.ModTime().After(d.ModificadoEm) {
			d.ModificadoEm = info.ModTime()
		}
	}
	return d, nil
}

// ApagarTemporarios apaga os temporários de gravação parados desde antes de
// limite, deixados por quedas, e devolve quantos. Uma falha num deles não
// impede os outros.
func (v *Volume) ApagarTemporarios(ctx context.Context, limite time.Time) (int, error) {
	ids, err := v.idsNoVolume()
	if err != nil {
		return 0, err
	}
	apagados, errRaiz := apagarAntigos(v.dir, limite, prefixoReadyz)
	erros := []error{errRaiz}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return apagados, err
		}
		n, err := apagarAntigos(v.dirVideo(id), limite, prefixoTemporario(nomeOriginal), prefixoTemporario(nomeZip))
		apagados += n
		erros = append(erros, err)
	}
	return apagados, errors.Join(erros...)
}

// apagarAntigos apaga os arquivos de dir que começam com um dos prefixos e
// não mudam desde antes de limite.
func apagarAntigos(dir string, limite time.Time, prefixos ...string) (int, error) {
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return 0, ignorarAusente("listar temporários", err)
	}
	apagados := 0
	var erros []error
	for _, e := range entradas {
		if !e.Type().IsRegular() || !temPrefixo(e.Name(), prefixos) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			erros = append(erros, ignorarAusente("ler temporário", err))
			continue
		}
		if !info.ModTime().Before(limite) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			erros = append(erros, ignorarAusente("apagar temporário", err))
			continue
		}
		apagados++
	}
	return apagados, errors.Join(erros...)
}

func temPrefixo(nome string, prefixos []string) bool {
	for _, p := range prefixos {
		if strings.HasPrefix(nome, p) {
			return true
		}
	}
	return false
}

// ApagarZip remove o Zip de frames do Vídeo; se ele já não existir, não é erro.
func (v *Volume) ApagarZip(_ context.Context, id uuid.UUID) error {
	return ignorarAusente("apagar zip", os.Remove(filepath.Join(v.dirVideo(id), nomeZip)))
}

// ApagarSeVazio remove o diretório do Vídeo se não houver mais nada nele.
func (v *Volume) ApagarSeVazio(_ context.Context, id uuid.UUID) error {
	dir := v.dirVideo(id)
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return ignorarAusente("ler diretório do vídeo", err)
	}
	if len(entradas) > 0 {
		return nil
	}
	// Remove, não RemoveAll: se algo entrou nele agora, falha em vez de apagar.
	return ignorarAusente("apagar diretório do vídeo", os.Remove(dir))
}

// ignorarAusente embrulha err, salvo se for o arquivo já não existir.
func ignorarAusente(operacao string, err error) error {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("%s: %w", operacao, err)
}
