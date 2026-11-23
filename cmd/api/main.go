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

	"github.com/redis/go-redis/v9"

	"github.com/LucazDenadai/projeto-fiapx/internal/api"
	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
	"github.com/LucazDenadai/projeto-fiapx/internal/limite"
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
	segredoJWT, err := processo.Exigir("JWT_SECRET")
	if err != nil {
		return err
	}
	redisURL, err := processo.Exigir("REDIS_URL")
	if err != nil {
		return err
	}
	opcoesRedis, err := redis.ParseURL(redisURL)
	if err != nil {
		return fmt.Errorf("REDIS_URL inválida: %w", err)
	}
	cfgLimite, err := configLimite()
	if err != nil {
		return err
	}
	dados := processo.Variavel("DATA_DIR", "/data")

	pool, err := banco.Abrir(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	tentativas := limite.Novo(opcoesRedis, cfgLimite, log)
	defer func() { _ = tentativas.Fechar() }()

	rotas, err := api.Novo(api.Config{
		Pool:       pool,
		SegredoJWT: []byte(segredoJWT),
		Log:        log,
		Tentativas: tentativas,
	})
	if err != nil {
		return err
	}
	publico := &http.Server{
		Addr:              processo.Variavel("HTTP_ADDR", ":8080"),
		Handler:           rotas,
		ReadHeaderTimeout: 5 * time.Second,
	}
	interno := processo.ServidorInterno(log,
		observabilidade.Dependencia{Nome: "postgres", Verificar: banco.Verificacao(pool)},
		observabilidade.Dependencia{Nome: "rabbitmq", Verificar: mensageria.Verificacao(amqpURL)},
		observabilidade.Dependencia{Nome: "volume", Verificar: armazenamento.Verificacao(dados)},
	)
	return processo.Servir(ctx, log, publico, interno)
}

// configLimite lê os limites de tentativas do ambiente (ADR-0009); o que não
// estiver definido fica com o padrão.
func configLimite() (limite.Config, error) {
	cfg := limite.Config{Regras: limite.RegrasPadrao(), Timeout: limite.TimeoutPadrao}
	var err error
	ler := func(r *limite.Regra, prefixo string) {
		if err != nil {
			return
		}
		if r.Max, err = processo.Inteiro(prefixo+"_MAX", r.Max); err != nil {
			return
		}
		r.Janela, err = processo.Duracao(prefixo+"_JANELA", r.Janela)
	}
	ler(&cfg.LoginPorEmail, "LIMITE_LOGIN_EMAIL")
	ler(&cfg.LoginPorIP, "LIMITE_LOGIN_IP")
	ler(&cfg.CadastroPorIP, "LIMITE_CADASTRO_IP")
	if err != nil {
		return limite.Config{}, err
	}
	cfg.Timeout, err = processo.Duracao("LIMITE_REDIS_TIMEOUT", cfg.Timeout)
	return cfg, err
}
