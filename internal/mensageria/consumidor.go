package mensageria

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Resultado é o que o Tratador decidiu sobre uma mensagem.
type Resultado int

const (
	// Confirmar dá ack: a mensagem sai da fila.
	Confirmar Resultado = iota
	// Devolver dá nack com requeue: a mensagem volta para a fila e a entrega
	// conta no delivery-limit (ADR-0004).
	Devolver
)

// Tratador processa o corpo de uma mensagem. O ctx é cancelado quando o
// consumidor para.
type Tratador func(ctx context.Context, corpo []byte) Resultado

// ConfigConsumo do Consumir; zero em um campo usa o padrão.
type ConfigConsumo struct {
	Fila       string
	Prefetch   int           // mensagens em andamento ao mesmo tempo (padrão 1)
	BackoffMax time.Duration // teto da espera entre reconexões (padrão 30 s)
}

// Consumir entrega as mensagens da fila ao Tratador, até Prefetch delas ao
// mesmo tempo, e dá ack ou nack conforme o Resultado. A cada conexão declara a
// topologia antes do Consume. Se a conexão cair, reconecta com backoff; as
// mensagens sem ack voltam para a fila pelo broker. Bloqueia até o ctx acabar
// e esperar os tratadores em andamento.
func Consumir(ctx context.Context, url string, cfg ConfigConsumo, tratar Tratador, log *slog.Logger) {
	if cfg.Prefetch <= 0 {
		cfg.Prefetch = 1
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 30 * time.Second
	}
	espera := time.Second
	for {
		conectou, err := consumirConexao(ctx, url, cfg, tratar, log)
		if ctx.Err() != nil {
			return
		}
		if conectou {
			espera = time.Second
		}
		log.WarnContext(ctx, "consumidor: reconectando", "fila", cfg.Fila, "erro", err, "espera", espera)
		select {
		case <-ctx.Done():
			return
		case <-time.After(espera):
		}
		espera = min(espera*2, cfg.BackoffMax)
	}
}

// consumirConexao consome por uma conexão até ela cair ou o ctx acabar.
// conectou diz se o Consume chegou a começar.
func consumirConexao(ctx context.Context, url string, cfg ConfigConsumo, tratar Tratador, log *slog.Logger) (conectou bool, err error) {
	ctxDiscar, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := discar(ctxDiscar, url)
	cancel()
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err == nil {
		err = declararTopologia(ch)
	}
	if err == nil {
		err = ch.Qos(cfg.Prefetch, 0, false)
	}
	if err != nil {
		return false, fmt.Errorf("preparar canal: %w", err)
	}
	entregas, err := ch.ConsumeWithContext(ctx, cfg.Fila, "", false, false, false, false, nil)
	if err != nil {
		return false, fmt.Errorf("consumir %s: %w", cfg.Fila, err)
	}

	// Executa antes do Close da conexão: os tratadores em andamento terminam
	// (e dão ack ou nack) antes de ela fechar.
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return true, nil
		case d, ok := <-entregas:
			if !ok {
				return true, errors.New("canal de consumo fechado")
			}
			wg.Go(func() { responder(ctx, d, tratar(ctx, d.Body), cfg.Fila, log) })
		}
	}
}

func responder(ctx context.Context, d amqp.Delivery, r Resultado, fila string, log *slog.Logger) {
	var err error
	if r == Confirmar {
		err = d.Ack(false)
	} else {
		err = d.Nack(false, true)
	}
	if err != nil {
		// Canal fechado: sem ack, o broker devolve a mensagem para a fila.
		log.WarnContext(ctx, "consumidor: não respondeu ao broker", "fila", fila, "resultado", r, "erro", err)
	}
}
