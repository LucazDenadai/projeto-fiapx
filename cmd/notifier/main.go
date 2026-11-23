// Comando notifier: envia a Notificação de Falha definitiva (próximos tickets).
// Por ora expõe só a porta interna de saúde.
//
//	notifier              sobe o Notifier
//	notifier healthcheck  sonda o próprio /readyz (healthcheck do Compose)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
	"github.com/LucazDenadai/projeto-fiapx/internal/processo"
)

func main() {
	log := observabilidade.NovoLogger("notifier", os.Stdout)
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
	amqpURL, err := processo.Exigir("RABBITMQ_URL")
	if err != nil {
		return err
	}
	interno := processo.ServidorInterno(log,
		observabilidade.Dependencia{Nome: "rabbitmq", Verificar: mensageria.Verificacao(amqpURL)},
	)
	return processo.Servir(ctx, log, interno)
}
