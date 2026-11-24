package mensageria

import (
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Topologia (ADR-0002, ADR-0004).
const (
	FilaProcessamento = "processamento"
	FilaNotificacao   = "notificacao"
	ExchangeEventos   = "processamento.eventos"
	EventoFalhou      = "falhou"
	EventoConcluido   = "concluido" // sem assinante por enquanto

	// LimiteEntregasPadrao é o delivery-limit padrão das filas de trabalho;
	// passado dele, a mensagem vai para a DLQ.
	LimiteEntregasPadrao = 5
)

// Topologia é a parte configurável das filas (ADR-0004). Zero num campo usa o
// padrão. API e Worker têm de usar os mesmos valores: o broker não muda os
// argumentos de uma fila que já existe.
type Topologia struct {
	LimiteEntregas int // delivery-limit das filas de trabalho (padrão 5)
}

func (t Topologia) comPadroes() Topologia {
	if t.LimiteEntregas <= 0 {
		t.LimiteEntregas = LimiteEntregasPadrao
	}
	return t
}

// DLQ devolve o nome da fila de dead-letter da fila dada.
func DLQ(fila string) string { return fila + ".dlq" }

// declararTopologia cria, se faltarem, as filas quorum com suas DLQs e a
// exchange de eventos. É idempotente: API e Worker a declaram ao conectar.
func declararTopologia(ch *amqp.Channel, top Topologia) error {
	top = top.comPadroes()
	for _, fila := range []string{FilaProcessamento, FilaNotificacao} {
		// A DLQ não tem delivery-limit (-1): o consumidor da processamento.dlq
		// tenta até conseguir, e a notificacao.dlq fica parada para inspeção.
		if _, err := ch.QueueDeclare(DLQ(fila), true, false, false, false, amqp.Table{
			"x-queue-type":     "quorum",
			"x-delivery-limit": -1,
		}); err != nil {
			return fmt.Errorf("declarar %s: %w", DLQ(fila), err)
		}
		if _, err := ch.QueueDeclare(fila, true, false, false, false, amqp.Table{
			"x-queue-type":              "quorum",
			"x-delivery-limit":          top.LimiteEntregas,
			"x-dead-letter-exchange":    "",
			"x-dead-letter-routing-key": DLQ(fila),
			// at-least-once: a mensagem só sai da fila depois de confirmada na
			// DLQ; exige reject-publish.
			"x-dead-letter-strategy": "at-least-once",
			"x-overflow":             "reject-publish",
		}); err != nil {
			if amqpErr, ok := errors.AsType[*amqp.Error](err); ok && amqpErr.Code == amqp.PreconditionFailed {
				return fmt.Errorf("a fila %s já existe com outros argumentos (delivery-limit %d pedido): "+
					"o broker não os muda; recrie as filas (docker compose down -v): %w", fila, top.LimiteEntregas, err)
			}
			return fmt.Errorf("declarar %s: %w", fila, err)
		}
	}
	if err := ch.ExchangeDeclare(ExchangeEventos, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declarar %s: %w", ExchangeEventos, err)
	}
	if err := ch.QueueBind(FilaNotificacao, EventoFalhou, ExchangeEventos, false, nil); err != nil {
		return fmt.Errorf("ligar %s a %s: %w", FilaNotificacao, EventoFalhou, err)
	}
	return nil
}
