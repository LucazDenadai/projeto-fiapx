package observabilidade

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Sondar faz GET na URL e devolve erro se ela não responder 200.
// É o healthcheck do Compose: as imagens distroless não têm curl nem wget.
func Sondar(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("sondar %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sondar %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sondar %s: status %d", url, resp.StatusCode)
	}
	return nil
}
