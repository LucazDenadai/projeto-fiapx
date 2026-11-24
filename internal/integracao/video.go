package integracao

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// ExigirFFmpeg pula o teste se o ffmpeg ou o ffprobe não estiverem no PATH.
// No CI e na imagem do Worker eles estão (ADR-0016).
func ExigirFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s fora do PATH", bin)
		}
	}
}

// Video gera com o testsrc do ffmpeg um MP4 pequeno com a duração dada e
// devolve o caminho dele, num diretório temporário do teste.
func Video(t *testing.T, duracao time.Duration) string {
	t.Helper()
	ExigirFFmpeg(t)
	saida := filepath.Join(t.TempDir(), "video.mp4")
	fonte := "testsrc=size=160x120:rate=10:duration=" + strconv.FormatFloat(duracao.Seconds(), 'f', -1, 64)
	cmd := exec.CommandContext(t.Context(), "ffmpeg", //nolint:gosec // argumentos do próprio teste
		"-nostdin", "-v", "error", "-f", "lavfi", "-i", fonte, "-pix_fmt", "yuv420p", saida)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gerar vídeo de teste: %v: %s", err, out)
	}
	return saida
}
