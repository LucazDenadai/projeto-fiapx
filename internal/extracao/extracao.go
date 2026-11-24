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

// entradaRestrita vai antes do arquivo de entrada no ffprobe e no ffmpeg: só
// os demuxers dos formatos que a API aceita (videos.pareceVideo) abrem o
// original, e só pelo protocolo file. Sem isso, um original que fosse, por
// exemplo, uma playlist HLS ou um script concat faria o ffmpeg ler outros
// arquivos do container ou URLs da rede. Referências externas do mov (dref)
// já vêm desligadas por padrão (enable_drefs=0); passar a opção explícita
// quebraria os outros demuxers, que não a conhecem.
var entradaRestrita = []string{
	"-protocol_whitelist", "file",
	"-format_whitelist", "mov,matroska,avi,mpeg,mpegts,flv,asf",
}

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

// ErrVideoInvalido: o ffprobe ou o ffmpeg não conseguem ler o arquivo como
// vídeo (formato fora da lista, corrompido, sem stream de vídeo). É problema
// do arquivo: tentar de novo dá o mesmo resultado (ADR-0004).
var ErrVideoInvalido = errors.New("arquivo ilegível como vídeo")

// ErrSemVideo: o arquivo abre, mas não tem stream de vídeo. É um
// ErrVideoInvalido.
var ErrSemVideo = fmt.Errorf("%w: sem stream de vídeo", ErrVideoInvalido)

// Sondar lê a duração do arquivo com o ffprobe e confere que ele tem um stream
// de vídeo.
func (e *Extrator) Sondar(ctx context.Context, arquivo string) (Sonda, error) {
	// O ffprobe também sai com erro se o arquivo não existe; isso é problema
	// do volume, não do vídeo, e não pode virar ErrVideoInvalido.
	if _, err := os.Stat(arquivo); err != nil {
		return Sonda{}, fmt.Errorf("original: %w", err)
	}
	args := append([]string{"-v", "error"}, entradaRestrita...)
	args = append(args,
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_type:format=duration",
		"-of", "json",
		"file:"+arquivo,
	)
	saida, err := e.rodar(ctx, e.cfg.FFprobe, args...)
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
	// Sem duração legível não dá para conferir a duração máxima (ADR-0011).
	segundos, err := strconv.ParseFloat(r.Format.Duration, 64)
	if err != nil || segundos < 0 {
		return Sonda{}, fmt.Errorf("%w: duração %q no ffprobe", ErrVideoInvalido, r.Format.Duration)
	}
	return Sonda{Duracao: time.Duration(segundos * float64(time.Second))}, nil
}

// ExtrairFrames grava em dir um frame PNG por segundo do primeiro stream de
// vídeo do arquivo e devolve os caminhos dos frames, em ordem. dir deve estar
// vazio.
func (e *Extrator) ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error) {
	args := append([]string{"-nostdin", "-hide_banner", "-v", "error"}, entradaRestrita...)
	args = append(args,
		"-i", "file:"+arquivo,
		"-map", "0:v:0",
		"-vf", "fps=1",
		"-f", "image2",
		"file:"+filepath.Join(dir, prefixoFrame+"%05d"+extensao),
	)
	if _, err := e.rodar(ctx, e.cfg.FFmpeg, args...); err != nil {
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
// prazo estourar, o erro embrulha context.DeadlineExceeded. Se o processo
// terminar por conta própria com código de erro, embrulha ErrVideoInvalido:
// os argumentos são fixos, então quem falhou foi a leitura do arquivo. Morto
// por sinal (OOM, kill) não conta, porque não diz nada do vídeo.
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
		detalhe := strings.TrimSpace(stderr.String())
		if saida, ok := errors.AsType[*exec.ExitError](err); ok && saida.ExitCode() > 0 {
			return nil, fmt.Errorf("%w (%w): %s", ErrVideoInvalido, err, detalhe)
		}
		return nil, fmt.Errorf("%w: %s", err, detalhe)
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
