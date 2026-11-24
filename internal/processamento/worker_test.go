package processamento_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
)

// Caminho inteiro: Job na fila processamento, consumidor, Processamento e ack.
func TestWorker_JobDaFilaTerminaConcluidoEConfirmado(t *testing.T) {
	c := novoCenario(t)
	url := integracao.RabbitMQ(t)
	id := c.videoRecebido(t, "recebido", 2*time.Second)

	ctx, cancelar := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		mensageria.Consumir(ctx, url, mensageria.ConfigConsumo{Fila: mensageria.FilaProcessamento},
			c.processador(t, nil, nil).Processar, slog.New(slog.DiscardHandler))
	})
	t.Cleanup(func() { cancelar(); wg.Wait() })

	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var ch *amqp.Channel
	for range 100 { // espera o consumidor declarar a fila
		if ch, err = conn.Channel(); err != nil {
			t.Fatal(err)
		}
		if _, err = ch.QueueDeclarePassive(mensageria.FilaProcessamento, true, false, false, false, nil); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("fila processamento não apareceu: %v", err)
	}
	if err := ch.PublishWithContext(t.Context(), "", mensageria.FilaProcessamento, true, false,
		amqp.Publishing{Body: mensagem(id, "corr")}); err != nil {
		t.Fatalf("publicar job: %v", err)
	}

	for range 200 {
		if c.status(t, id) == "concluido" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s := c.status(t, id); s != "concluido" {
		t.Fatalf("status = %s, quero concluido", s)
	}
	for range 100 {
		q, err := ch.QueueDeclarePassive(mensageria.FilaProcessamento, true, false, false, false, nil)
		if err != nil {
			t.Fatal(err)
		}
		if q.Messages == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("mensagem não foi confirmada")
}
