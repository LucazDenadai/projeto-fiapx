// Comando api: porta pública de negócio, porta interna de saúde e o subcomando
// migrate, usado pelo serviço de execução única do Compose (ADR-0016).
//
//	api              sobe a API
//	api migrate      aplica as migrations e sai
//	api healthcheck  sonda o próprio /readyz (healthcheck do Compose)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
	"github.com/LucazDenadai/projeto-fiapx/internal/processo"
)

func main() {
	log := observabilidade.NovoLogger("api", os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	var err error
	switch sub := processo.Subcomando(); sub {
	case "":
		err = servir(ctx, log)
	case "migrate":
		err = migrar(ctx, log)
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

func migrar(ctx context.Context, log *slog.Logger) error {
	url, err := processo.Exigir("DATABASE_URL")
	if err != nil {
		return err
	}
	pool, err := banco.Abrir(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := banco.Migrar(ctx, pool); err != nil {
		return err
	}
	log.Info("migrations aplicadas")
	return nil
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
	dados := processo.Variavel("DATA_DIR", "/data")

	pool, err := banco.Abrir(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	publico := &http.Server{
		Addr:              processo.Variavel("HTTP_ADDR", ":8080"),
		Handler:           http.NewServeMux(), // rotas de negócio chegam nos próximos tickets
		ReadHeaderTimeout: 5 * time.Second,
	}
	interno := processo.ServidorInterno(log,
		observabilidade.Dependencia{Nome: "postgres", Verificar: banco.Verificacao(pool)},
		observabilidade.Dependencia{Nome: "rabbitmq", Verificar: mensageria.Verificacao(amqpURL)},
		observabilidade.Dependencia{Nome: "volume", Verificar: armazenamento.Verificacao(dados)},
	)
	return processo.Servir(ctx, log, publico, interno)
}
