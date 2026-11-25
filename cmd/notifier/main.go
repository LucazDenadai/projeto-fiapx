// Comando notifier: consome a fila notificacao e avisa o Usuário da Falha
// definitiva por e-mail (ADR-0008). Não acessa o Postgres.
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
	"sync"
	"syscall"

	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/notificacao"
	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
	"github.com/LucazDenadai/projeto-fiapx/internal/processo"
	"github.com/LucazDenadai/projeto-fiapx/internal/retentativa"
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
	smtpEndereco, err := processo.Exigir("SMTP_ENDERECO")
	if err != nil {
		return err
	}
	remetente := processo.Variavel("SMTP_REMETENTE", "nao-responda@fiapx.local")
	if err := notificacao.ValidarRemetente(remetente); err != nil {
		return err
	}
	timeoutEnvio, err := processo.Duracao("SMTP_TIMEOUT", notificacao.TimeoutEnvioPadrao)
	if err != nil {
		return err
	}
	// Retry e DLQ (ADR-0004): os mesmos números do Worker.
	limiteEntregas, err := processo.Inteiro("DELIVERY_LIMIT", mensageria.LimiteEntregasPadrao)
	if err != nil {
		return err
	}
	backoffInicial, err := processo.Duracao("RETRY_BACKOFF_INICIAL", retentativa.InicialPadrao)
	if err != nil {
		return err
	}
	orcamentoRetry, err := processo.Duracao("RETRY_ORCAMENTO", retentativa.OrcamentoPadrao)
	if err != nil {
		return err
	}

	smtp := notificacao.NovoSMTP(notificacao.ConfigSMTP{
		Endereco: smtpEndereco,
		Usuario:  os.Getenv("SMTP_USUARIO"),
		Senha:    os.Getenv("SMTP_SENHA"),
	})
	registro := observabilidade.NovoRegistro()
	n := notificacao.NovoNotificador(smtp, notificacao.Config{
		Remetente:    remetente,
		TimeoutEnvio: timeoutEnvio,
		Retry:        retentativa.Politica{Inicial: backoffInicial, Orcamento: orcamentoRetry},
		Registro:     registro,
	}, log)

	ctxConsumo, pararConsumo := context.WithCancel(ctx)
	var consumo sync.WaitGroup
	consumo.Go(func() {
		mensageria.Consumir(ctxConsumo, amqpURL, mensageria.ConfigConsumo{
			Fila:      mensageria.FilaNotificacao,
			Topologia: mensageria.Topologia{LimiteEntregas: int(limiteEntregas)},
		}, n.Notificar, log)
	})
	// A notificacao.dlq não tem consumidor: fica parada para inspeção.
	defer func() {
		pararConsumo()
		consumo.Wait()
	}()

	interno := processo.ServidorInterno(log, registro,
		observabilidade.Dependencia{Nome: "rabbitmq", Verificar: mensageria.Verificacao(amqpURL)},
		observabilidade.Dependencia{Nome: "smtp", Verificar: notificacao.Verificacao(smtpEndereco)},
	)
	return processo.Servir(ctx, log, interno)
}
