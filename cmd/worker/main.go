// Comando worker: consome a fila de Processamento (próximos tickets). Por ora
// roda o relay da outbox e expõe a porta interna de saúde.
//
//	worker              sobe o Worker
//	worker healthcheck  sonda o próprio /readyz (healthcheck do Compose)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
	"github.com/LucazDenadai/projeto-fiapx/internal/outbox"
	"github.com/LucazDenadai/projeto-fiapx/internal/processo"
)

func main() {
	log := observabilidade.NovoLogger("worker", os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	var err error
	switch sub := processo.Subcomando(); sub {
	case "":
		err = servir(ctx, log)
	case "healthcheck":
		err = processo.Healthcheck(ctx)
	default:
		err = fmt.Errorf("subcomando desconhecido %q", sub)
	}
	stop()
	if err != nil {
		log.Error("encerrando com erro", "erro", err)
		os.Exit(1)
	}
}

func servir(ctx context.Context, log *slog.Logger) error {
	dbURL, err := processo.Exigir("DATABASE_URL")
	if err != nil {
		return err
	}
	amqpURL, err := processo.Exigir("RABBITMQ_URL")
	if err != nil {
		return err
	}
	intervaloOutbox, err := processo.Duracao("OUTBOX_INTERVALO", time.Second)
	if err != nil {
		return err
	}
	pool, err := banco.Abrir(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Relay da outbox (ADR-0003): publica os eventos que o Worker grava.
	pararRelay := outbox.Subir(ctx, pool, amqpURL, outbox.Config{Intervalo: intervaloOutbox}, log)
	defer pararRelay()

	interno := processo.ServidorInterno(log,
		observabilidade.Dependencia{Nome: "postgres", Verificar: banco.Verificacao(pool)},
		observabilidade.Dependencia{Nome: "rabbitmq", Verificar: mensageria.Verificacao(amqpURL)},
	)
	return processo.Servir(ctx, log, interno)
}
