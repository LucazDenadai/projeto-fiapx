// Package extracao roda o ffprobe e o ffmpeg sobre o original de um Vídeo:
// 1 frame por segundo, em PNG (ADR-0011).
package extracao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TimeoutPadrao é o tempo máximo de cada execução do ffprobe ou do ffmpeg.
const TimeoutPadrao = 10 * time.Minute

// Só os demuxers dos formatos que a API aceita (videos.pareceVideo) abrem o
// original, e só pelo protocolo file. Sem isso, um original que fosse, por
// exemplo, uma playlist HLS ou um script concat faria o ffmpeg ler outros
// arquivos do container ou URLs da rede.
const (
	demuxersPermitidos   = "mov,matroska,avi,mpeg,mpegts,flv,asf"
	protocolosPermitidos = "file"
)

// prefixoFrame e extensao nomeiam os frames gerados: frame_00001.png, ...
const (
	prefixoFrame = "frame_"
	extensao     = ".png"
)

// Config do Extrator; zero em um campo usa o padrão.
type Config struct {
	Timeout time.Duration // por execução (padrão TimeoutPadrao)
	FFmpeg  string        // binário (padrão "ffmpeg", do PATH)
	FFprobe string        // binário (padrão "ffprobe", do PATH)
}

// Extrator roda o ffprobe e o ffmpeg com timeout.
type Extrator struct {
	cfg Config
}

// Novo monta o Extrator.
func Novo(cfg Config) *Extrator {
	if cfg.Timeout <= 0 {
		cfg.Timeout = TimeoutPadrao
	}
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
	if cfg.FFprobe == "" {
		cfg.FFprobe = "ffprobe"
	}
	return &Extrator{cfg: cfg}
}

// Sonda é o que o ffprobe diz do arquivo.
type Sonda struct {
	Duracao time.Duration
}

// ErrSemVideo: o arquivo abre, mas não tem stream de vídeo.
var ErrSemVideo = errors.New("arquivo sem stream de vídeo")

// Sondar lê a duração do arquivo com o ffprobe e confere que ele tem um stream
// de vídeo.
func (e *Extrator) Sondar(ctx context.Context, arquivo string) (Sonda, error) {
	saida, err := e.rodar(ctx, e.cfg.FFprobe,
		"-v", "error",
		"-protocol_whitelist", protocolosPermitidos,
		"-format_whitelist", demuxersPermitidos,
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_type:format=duration",
		"-of", "json",
		"file:"+arquivo,
	)
	if err != nil {
		return Sonda{}, fmt.Errorf("ffprobe: %w", err)
	}
	var r struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(saida, &r); err != nil {
		return Sonda{}, fmt.Errorf("ler saída do ffprobe: %w", err)
	}
	if len(r.Streams) == 0 {
		return Sonda{}, ErrSemVideo
	}
	segundos, err := strconv.ParseFloat(r.Format.Duration, 64)
	if err != nil {
		return Sonda{}, fmt.Errorf("duração %q do ffprobe: %w", r.Format.Duration, err)
	}
	return Sonda{Duracao: time.Duration(segundos * float64(time.Second))}, nil
}

// ExtrairFrames grava em dir um frame PNG por segundo do primeiro stream de
// vídeo do arquivo e devolve os caminhos dos frames, em ordem. dir deve estar
// vazio.
func (e *Extrator) ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error) {
	_, err := e.rodar(ctx, e.cfg.FFmpeg,
		"-nostdin", "-hide_banner", "-v", "error",
		"-protocol_whitelist", protocolosPermitidos,
		"-format_whitelist", demuxersPermitidos,
		"-i", "file:"+arquivo,
		"-map", "0:v:0",
		"-vf", "fps=1",
		"-f", "image2",
		"file:"+filepath.Join(dir, prefixoFrame+"%05d"+extensao),
	)
	if err != nil {
		return nil, fmt.Errorf("ffmpeg: %w", err)
	}
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listar frames: %w", err)
	}
	var frames []string
	for _, ent := range entradas {
		nome := ent.Name()
		if ent.Type().IsRegular() && strings.HasPrefix(nome, prefixoFrame) && strings.HasSuffix(nome, extensao) {
			frames = append(frames, filepath.Join(dir, nome))
		}
	}
	slices.Sort(frames) // o número tem largura fixa: ordem de nome é ordem de tempo
	return frames, nil
}

// tamMaxErro é quanto da saída de erro do processo vai para a mensagem de
// erro (só log, nunca o Usuário: ADR-0004).
const tamMaxErro = 4 << 10

// rodar executa o binário com o timeout do Extrator e devolve o stdout. Se o
// prazo estourar, o erro embrulha context.DeadlineExceeded.
func (e *Extrator) rodar(ctx context.Context, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	// bin vem da configuração; os argumentos são fixos e os caminhos, gerados
	// pelo servidor (ADR-0005).
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // ver acima
	cmd.WaitDelay = 5 * time.Second               // depois do kill, não espera pipes para sempre
	var stdout strings.Builder
	stderr := &ultimos{max: tamMaxErro}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w (%w)", ctx.Err(), err)
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return []byte(stdout.String()), nil
}

// ultimos guarda só os últimos max bytes escritos.
type ultimos struct {
	max int
	buf []byte
}

func (u *ultimos) Write(p []byte) (int, error) {
	u.buf = append(u.buf, p...)
	if len(u.buf) > u.max {
		u.buf = u.buf[len(u.buf)-u.max:]
	}
	return len(p), nil
}

func (u *ultimos) String() string { return string(u.buf) }
