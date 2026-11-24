// Package mensageria fala com o RabbitMQ (ADR-0002).
package mensageria

import "context"

// Verificacao devolve a checagem de readiness do RabbitMQ: abre uma conexão
// AMQP dentro do prazo do contexto e a fecha.
func Verificacao(url string) func(context.Context) error {
	return func(ctx context.Context) error {
		conn, err := discar(ctx, url)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	}
}
