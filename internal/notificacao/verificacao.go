package notificacao

import (
	"context"
	"fmt"
	"net"
)

// ValidarRemetente confere, na subida, que o remetente é um endereço simples.
func ValidarRemetente(remetente string) error {
	if _, err := endereco(remetente); err != nil {
		return fmt.Errorf("remetente %q: %w", remetente, err)
	}
	return nil
}

// Verificacao devolve a checagem de readiness do servidor SMTP: abre uma
// conexão TCP dentro do prazo do contexto e a fecha.
func Verificacao(enderecoSMTP string) func(context.Context) error {
	return func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", enderecoSMTP)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	}
}
