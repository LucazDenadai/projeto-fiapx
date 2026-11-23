// Package processo tem o que os três binários fazem igual: ler configuração do
// ambiente, servir HTTP até o sinal de parada e responder ao healthcheck.
package processo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
)

// PortaInternaPadrao é a porta de /healthz e /readyz (ADR-0010), nunca publicada.
const PortaInternaPadrao = ":9090"

// Variavel lê uma variável de ambiente, com valor padrão.
func Variavel(nome, padrao string) string {
	if v := os.Getenv(nome); v != "" {
		return v
	}
	return padrao
}

// Inteiro lê uma variável de ambiente inteira e positiva, com valor padrão.
func Inteiro(nome string, padrao int64) (int64, error) {
	v := os.Getenv(nome)
	if v == "" {
		return padrao, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("variável de ambiente %s: %w", nome, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("variável de ambiente %s=%q: quero inteiro positivo", nome, v)
	}
	return n, nil
}

// Duracao lê uma variável de ambiente de duração positiva (ex.: 15m, 1h), com
// valor padrão.
func Duracao(nome string, padrao time.Duration) (time.Duration, error) {
	v := os.Getenv(nome)
	if v == "" {
		return padrao, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("variável de ambiente %s: %w", nome, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("variável de ambiente %s=%q: quero duração positiva (ex.: 15m)", nome, v)
	}
	return d, nil
}

// Exigir lê uma variável de ambiente obrigatória.
func Exigir(nome string) (string, error) {
	v := os.Getenv(nome)
	if v == "" {
		return "", fmt.Errorf("variável de ambiente %s não definida", nome)
	}
	return v, nil
}

// Subcomando devolve o primeiro argumento da linha de comando, ou "".
func Subcomando() string {
	if len(os.Args) > 1 {
		return os.Args[1]
	}
	return ""
}

// Servir roda os servidores até o ctx ser cancelado e então os desliga com prazo.
// Se um deles parar sozinho, desliga os outros e devolve o erro.
func Servir(ctx context.Context, log *slog.Logger, servidores ...*http.Server) error {
	erros := make(chan error, len(servidores))
	for _, s := range servidores {
		go func() {
			log.Info("servidor no ar", "endereco", s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				erros <- fmt.Errorf("servidor %s: %w", s.Addr, err)
				return
			}
			erros <- nil
		}()
	}

	var primeiro error
	select {
	case <-ctx.Done():
		log.Info("desligando")
	case primeiro = <-erros:
	}

	ctxParada, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	for _, s := range servidores {
		if err := s.Shutdown(ctxParada); err != nil && primeiro == nil {
			primeiro = fmt.Errorf("desligar %s: %w", s.Addr, err)
		}
	}
	return primeiro
}

// ServidorInterno monta o servidor da porta interna com as dependências dadas.
func ServidorInterno(log *slog.Logger, deps ...observabilidade.Dependencia) *http.Server {
	return &http.Server{
		Addr:              Variavel("INTERNAL_ADDR", PortaInternaPadrao),
		Handler:           observabilidade.HandlerInterno(log, deps...),
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// Healthcheck sonda o /readyz da própria porta interna. É o subcomando usado
// pelo healthcheck do Compose.
func Healthcheck(ctx context.Context) error {
	_, porta, err := net.SplitHostPort(Variavel("INTERNAL_ADDR", PortaInternaPadrao))
	if err != nil {
		return fmt.Errorf("INTERNAL_ADDR inválido: %w", err)
	}
	return observabilidade.Sondar(ctx, "http://"+net.JoinHostPort("127.0.0.1", porta)+"/readyz")
}
