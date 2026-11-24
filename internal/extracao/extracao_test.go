package extracao_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/extracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
)

func TestExtrairFrames_UmFramePorSegundoEmPNG(t *testing.T) {
	video := integracao.Video(t, 5*time.Second)
	dir := t.TempDir()

	frames, err := extracao.Novo(extracao.Config{}).ExtrairFrames(t.Context(), video, dir)
	if err != nil {
		t.Fatalf("extrair: %v", err)
	}
	if len(frames) != 5 {
		t.Fatalf("frames = %d (%v), quero 5", len(frames), frames)
	}
	for i, f := range frames {
		if filepath.Dir(f) != dir || filepath.Ext(f) != ".png" {
			t.Errorf("frame %d = %s, quero PNG em %s", i, f, dir)
		}
		cabecalho := make([]byte, 8)
		arq, err := os.Open(f) //nolint:gosec // frame gerado pelo teste
		if err != nil {
			t.Fatalf("abrir frame: %v", err)
		}
		_, _ = arq.Read(cabecalho)
		_ = arq.Close()
		if string(cabecalho[1:4]) != "PNG" {
			t.Errorf("frame %s não é PNG", f)
		}
	}
	if frames[0] > frames[4] {
		t.Errorf("frames fora de ordem: %v", frames)
	}
}

func TestSondar_DevolveDuracao(t *testing.T) {
	video := integracao.Video(t, 3*time.Second)

	s, err := extracao.Novo(extracao.Config{}).Sondar(t.Context(), video)
	if err != nil {
		t.Fatalf("sondar: %v", err)
	}
	if s.Duracao < 2900*time.Millisecond || s.Duracao > 3100*time.Millisecond {
		t.Errorf("duração = %v, quero ~3s", s.Duracao)
	}
}

// Uma playlist HLS disfarçada de vídeo faria o ffmpeg ler outros arquivos ou
// URLs; só os demuxers dos formatos aceitos na API podem abrir o original.
func TestSondarEExtrair_RecusamFormatoForaDaLista(t *testing.T) {
	integracao.ExigirFFmpeg(t)
	alvo := filepath.Join(t.TempDir(), "segredo.ts")
	if err := os.WriteFile(alvo, []byte("segredo"), 0o600); err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(t.TempDir(), "original")
	conteudo := "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\n" + alvo + "\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(playlist, []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
	ext := extracao.Novo(extracao.Config{})

	if _, err := ext.Sondar(t.Context(), playlist); err == nil {
		t.Error("sondar uma playlist HLS deveria falhar")
	}
	dir := t.TempDir()
	if _, err := ext.ExtrairFrames(t.Context(), playlist, dir); err == nil {
		t.Error("extrair de uma playlist HLS deveria falhar")
	}
}

func TestExtrairFrames_TimeoutMataOFFmpeg(t *testing.T) {
	video := integracao.Video(t, 5*time.Second)

	inicio := time.Now()
	_, err := extracao.Novo(extracao.Config{Timeout: time.Nanosecond}).ExtrairFrames(t.Context(), video, t.TempDir())
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, extracao.ErrVideoInvalido) {
		t.Fatalf("erro = %v, quero context.DeadlineExceeded (e não ErrVideoInvalido)", err)
	}
	if d := time.Since(inicio); d > 5*time.Second {
		t.Errorf("demorou %v para desistir", d)
	}
}

// Arquivo que o ffprobe ou o ffmpeg não conseguem ler é problema do vídeo
// (Falha definitiva), não de infraestrutura.
func TestSondarEExtrair_ArquivoQueNaoEhVideoEhInvalido(t *testing.T) {
	integracao.ExigirFFmpeg(t)
	lixo := filepath.Join(t.TempDir(), "original")
	if err := os.WriteFile(lixo, []byte("isto não é um vídeo"), 0o600); err != nil {
		t.Fatal(err)
	}
	ext := extracao.Novo(extracao.Config{})
	if _, err := ext.Sondar(t.Context(), lixo); !errors.Is(err, extracao.ErrVideoInvalido) {
		t.Errorf("sondar: erro = %v, quero ErrVideoInvalido", err)
	}
	if _, err := ext.ExtrairFrames(t.Context(), lixo, t.TempDir()); !errors.Is(err, extracao.ErrVideoInvalido) {
		t.Errorf("extrair: erro = %v, quero ErrVideoInvalido", err)
	}
}

func TestSondar_SemStreamDeVideoEhInvalido(t *testing.T) {
	integracao.ExigirFFmpeg(t)
	audio := filepath.Join(t.TempDir(), "audio.mkv")
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-v", "error", //nolint:gosec // argumentos do teste
		"-f", "lavfi", "-i", "sine=duration=1", audio)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gerar áudio: %v: %s", err, out)
	}
	_, err := extracao.Novo(extracao.Config{}).Sondar(t.Context(), audio)
	if !errors.Is(err, extracao.ErrSemVideo) || !errors.Is(err, extracao.ErrVideoInvalido) {
		t.Errorf("erro = %v, quero ErrSemVideo, que é ErrVideoInvalido", err)
	}
}

// Original sumido é problema do volume: o Processamento tenta de novo.
func TestSondar_ArquivoAusenteNaoEhInvalido(t *testing.T) {
	integracao.ExigirFFmpeg(t)
	_, err := extracao.Novo(extracao.Config{}).Sondar(t.Context(), filepath.Join(t.TempDir(), "sumiu"))
	if err == nil || errors.Is(err, extracao.ErrVideoInvalido) {
		t.Errorf("erro = %v, quero erro que não seja ErrVideoInvalido", err)
	}
}
