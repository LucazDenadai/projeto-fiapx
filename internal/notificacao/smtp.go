package notificacao

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
)

// ConfigSMTP do servidor de saída (ADR-0008). No Compose, o Mailpit.
type ConfigSMTP struct {
	Endereco string // host:porta
	// Usuario e Senha são opcionais; com eles, AUTH PLAIN, que o net/smtp só
	// aceita sobre TLS (ou em localhost).
	Usuario string
	Senha   string
}

// SMTP envia pelo servidor configurado. Cada envio abre a própria conexão:
// um e-mail por Falha definitiva não justifica manter conexão aberta.
type SMTP struct {
	cfg ConfigSMTP
}

// NovoSMTP não conecta; a conexão abre em Enviar.
func NovoSMTP(cfg ConfigSMTP) *SMTP {
	return &SMTP{cfg: cfg}
}

// Enviar entrega msg de de para para. O prazo e o cancelamento de ctx valem
// para a conexão inteira. Usa STARTTLS quando o servidor oferece.
func (s *SMTP) Enviar(ctx context.Context, de, para string, msg []byte) (err error) {
	host, _, err := net.SplitHostPort(s.cfg.Endereco)
	if err != nil {
		return fmt.Errorf("endereço do smtp: %w", err)
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.cfg.Endereco)
	if err != nil {
		return fmt.Errorf("conectar ao smtp: %w", err)
	}
	if prazo, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(prazo)
	}
	// O net/smtp não conhece contexto: fechar a conexão destrava a conversa.
	parar := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer parar()

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: %w", err)
	}
	defer func() { _ = c.Close() }()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if s.cfg.Usuario != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Usuario, s.cfg.Senha, host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(de); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(para); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if err := c.Quit(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}
