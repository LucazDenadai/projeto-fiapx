// Comando pico dispara envios de Vídeo em paralelo contra a stack no ar e
// acompanha os Processamentos até o fim, para a demonstração de escala. Precisa
// do ffmpeg no PATH para gerar o vídeo, ou de -video.
//
//	go run ./cmd/pico                  20 envios de um vídeo de 60 s
//	go run ./cmd/pico -n 10 -video x.mp4
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/pico"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := rodar(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pico:", err)
		os.Exit(1)
	}
}

func rodar(ctx context.Context) error {
	url := flag.String("url", "http://localhost:8080", "endereço da API")
	n := flag.Int("n", 20, "envios em paralelo")
	video := flag.String("video", "", "arquivo a enviar (padrão: gerado com o testsrc do ffmpeg)")
	duracao := flag.Duration("duracao", time.Minute, "duração do vídeo gerado")
	semEsperar := flag.Bool("sem-esperar", false, "só envia, sem acompanhar os Processamentos")
	flag.Parse()
	if *n < 1 {
		return errors.New("-n tem de ser ao menos 1")
	}

	if *video == "" {
		dir, err := os.MkdirTemp("", "fiapx-pico-*")
		if err != nil {
			return fmt.Errorf("criar temporário: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		*video = filepath.Join(dir, "pico.mp4")
		if err := gerar(ctx, *video, *duracao); err != nil {
			return err
		}
	}

	r, err := pico.Rodar(ctx, pico.Config{URL: *url, Video: *video, Envios: *n, Esperar: !*semEsperar}, os.Stdout)
	if err != nil {
		return err
	}
	if !*semEsperar && r.Aceitos > 0 {
		fmt.Printf("%d Vídeos em %v: %.1f por minuto (%d concluídos, %d falhos)\n", r.Aceitos,
			r.DuracaoTotal.Round(100*time.Millisecond), float64(r.Aceitos)/r.DuracaoTotal.Minutes(), r.Concluidos, r.Falhos)
	}
	return nil
}

// gerar grava um MP4 Full HD com o testsrc do ffmpeg: pesado o bastante para
// a fila aparecer, leve para enviar.
func gerar(ctx context.Context, destino string, duracao time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", //nolint:gosec // argumentos da linha de comando de quem roda
		"-i", fmt.Sprintf("testsrc=size=1920x1080:rate=30:duration=%g", duracao.Seconds()),
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", destino)
	if saida, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gerar vídeo com o ffmpeg (instale-o ou use -video): %w: %s", err, saida)
	}
	return nil
}
