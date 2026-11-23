// Package observabilidade reúne o que todo processo expõe para ser observado:
// logs JSON e a porta interna com /healthz e /readyz (ADR-0010).
package observabilidade

import (
	"io"
	"log/slog"
)

// NovoLogger devolve um logger slog JSON em que toda linha leva o nome do serviço.
func NovoLogger(servico string, w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil)).With("servico", servico)
}
