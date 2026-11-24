// Comando worker: consome a fila processamento (extrai os frames e publica o
// Zip de frames) e a processamento.dlq (Falha definitiva das tentativas
// esgotadas), roda o relay da outbox e expõe a porta interna de saúde.
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
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
	"github.com/LucazDenadai/projeto-fiapx/internal/extracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
	"github.com/LucazDenadai/projeto-fiapx/internal/outbox"
	"github.com/LucazDenadai/projeto-fiapx/internal/processamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/processo"
	"github.com/LucazDenadai/projeto-fiapx/internal/retentativa"
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
	// Concorrência e limites (ADR-0011) e retenção do zip (ADR-0015).
	prefetch, err := processo.Inteiro("WORKER_PREFETCH", 1)
	if err != nil {
		return err
	}
	timeoutFFmpeg, err := processo.Duracao("FFMPEG_TIMEOUT", extracao.TimeoutPadrao)
	if err != nil {
		return err
	}
	retencaoZip, err := processo.Duracao("ZIP_RETENCAO", processamento.RetencaoZipPadrao)
	if err != nil {
		return err
	}
	duracaoMaxima, err := processo.Duracao("VIDEO_DURACAO_MAXIMA", processamento.DuracaoMaximaPadrao)
	if err != nil {
		return err
	}
	// Retry e DLQ (ADR-0004). DELIVERY_LIMIT tem de ser o mesmo da API.
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
	topologia := mensageria.Topologia{LimiteEntregas: int(limiteEntregas)}
	avisarConsumerTimeout(log, timeoutFFmpeg, orcamentoRetry)
	dados := processo.Variavel("DATA_DIR", "/data")
	// Disco efêmero do container, nunca o volume (ADR-0005).
	trabalho := processo.Variavel("TRABALHO_DIR", filepath.Join(os.TempDir(), "fiapx-worker"))

	pool, err := banco.Abrir(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Relay da outbox (ADR-0003): publica os eventos que o Worker grava.
	pararRelay := outbox.Subir(ctx, pool, amqpURL, outbox.Config{Intervalo: intervaloOutbox, Topologia: topologia}, log)
	defer pararRelay()

	proc := processamento.NovoProcessador(pool, armazenamento.NovoVolume(dados),
		extracao.Novo(extracao.Config{Timeout: timeoutFFmpeg}),
		processamento.Config{
			DirTrabalho:     trabalho,
			RetencaoZip:     retencaoZip,
			TimeoutExtracao: timeoutFFmpeg,
			DuracaoMaxima:   duracaoMaxima,
			Retry:           retentativa.Politica{Inicial: backoffInicial, Orcamento: orcamentoRetry},
		}, log)
	if err := proc.LimparTrabalho(ctx); err != nil {
		return err
	}
	ctxConsumo, pararConsumo := context.WithCancel(ctx)
	var consumo sync.WaitGroup
	consumo.Go(func() {
		mensageria.Consumir(ctxConsumo, amqpURL, mensageria.ConfigConsumo{
			Fila:      mensageria.FilaProcessamento,
			Prefetch:  int(prefetch),
			Topologia: topologia,
		}, proc.Processar, log)
	})
	// Consumidor da DLQ: fecha a Falha definitiva das mensagens que esgotaram
	// as entregas (ADR-0004).
	consumo.Go(func() {
		mensageria.Consumir(ctxConsumo, amqpURL, mensageria.ConfigConsumo{
			Fila:      mensageria.DLQ(mensageria.FilaProcessamento),
			Topologia: topologia,
		}, proc.ProcessarDLQ, log)
	})
	// Para de consumir e espera os Processamentos em andamento antes de
	// parar o relay e fechar o pool.
	defer func() {
		pararConsumo()
		consumo.Wait()
	}()

	interno := processo.ServidorInterno(log,
		observabilidade.Dependencia{Nome: "postgres", Verificar: banco.Verificacao(pool)},
		observabilidade.Dependencia{Nome: "rabbitmq", Verificar: mensageria.Verificacao(amqpURL)},
		observabilidade.Dependencia{Nome: "volume", Verificar: armazenamento.Verificacao(dados)},
	)
	return processo.Servir(ctx, log, interno)
}

// consumerTimeoutRabbitMQ é o padrão do broker: uma mensagem sem ack por mais
// tempo que isso fecha o canal e conta uma entrega espúria.
const consumerTimeoutRabbitMQ = 30 * time.Minute

// avisarConsumerTimeout avisa se, no pior caso, um Processamento passa do
// consumer_timeout: o ffmpeg mais o orçamento de cada operação de
// infraestrutura com retry antes de responder (ler original, iniciar,
// publicar zip, concluir ou registrar a falha) (ADR-0004).
func avisarConsumerTimeout(log *slog.Logger, timeoutFFmpeg, orcamento time.Duration) {
	if pior := timeoutFFmpeg + 4*orcamento; pior >= consumerTimeoutRabbitMQ {
		log.Warn("FFMPEG_TIMEOUT + 4×RETRY_ORCAMENTO passa do consumer_timeout do RabbitMQ; o broker pode derrubar o canal no meio de um Processamento",
			"pior_caso", pior.String(), "consumer_timeout", consumerTimeoutRabbitMQ.String())
	}
}
