package mensageria_test

import (
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
)

func TestMain(m *testing.M) { integracao.Principal(m) }

// canal abre uma conexão de teste, fora do Publicador.
func canal(t *testing.T, url string) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("canal: %v", err)
	}
	return ch
}

// receber espera uma mensagem na fila.
func receber(t *testing.T, ch *amqp.Channel, fila string) amqp.Delivery {
	t.Helper()
	for range 50 {
		d, ok, err := ch.Get(fila, false)
		if err != nil {
			t.Fatalf("get %s: %v", fila, err)
		}
		if ok {
			return d
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("nenhuma mensagem em %s", fila)
	return amqp.Delivery{}
}

func conectado(t *testing.T) string {
	t.Helper()
	url := integracao.RabbitMQ(t)
	pub := mensageria.NovoPublicador(url)
	t.Cleanup(pub.Fechar)
	if err := pub.Conectar(t.Context()); err != nil {
		t.Fatalf("conectar publicador: %v", err)
	}
	return url
}

func TestTopologia_FilasQuorumDeclaradasAoConectar(t *testing.T) {
	url := conectado(t)
	for _, fila := range []string{"processamento", "processamento.dlq", "notificacao", "notificacao.dlq"} {
		ch := canal(t, url)
		// Redeclarar como classic tem de falhar: a fila existe e é quorum.
		_, err := ch.QueueDeclare(fila, true, false, false, false, amqp.Table{"x-queue-type": "classic"})
		var amqpErr *amqp.Error
		if !errors.As(err, &amqpErr) || amqpErr.Code != amqp.PreconditionFailed {
			t.Errorf("%s: redeclarar como classic deu %v, quero PRECONDITION_FAILED", fila, err)
		}
	}
}

func TestTopologia_FalhouChegaNaFilaNotificacao(t *testing.T) {
	url := conectado(t)
	ch := canal(t, url)
	err := ch.PublishWithContext(t.Context(), "processamento.eventos", "falhou", true, false,
		amqp.Publishing{Body: []byte(`{"video_id":"x"}`)})
	if err != nil {
		t.Fatalf("publicar: %v", err)
	}
	if d := receber(t, ch, "notificacao"); string(d.Body) != `{"video_id":"x"}` {
		t.Errorf("corpo = %s", d.Body)
	}
}

func TestTopologia_ExcedeuEntregasVaiParaDLQ(t *testing.T) {
	url := conectado(t)
	ch := canal(t, url)
	if err := ch.PublishWithContext(t.Context(), "", "processamento", true, false, amqp.Publishing{Body: []byte("job")}); err != nil {
		t.Fatalf("publicar: %v", err)
	}

	// Cada nack com requeue conta uma entrega; passado o limite (5), a
	// mensagem sai da fila pela dead-letter.
	entregas := 0
	for entregas < 10 {
		d, ok, err := ch.Get("processamento", false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !ok {
			if entregas > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		entregas++
		_ = d.Nack(false, true)
		time.Sleep(50 * time.Millisecond)
	}
	if entregas < 5 || entregas > 6 {
		t.Errorf("entregas antes da DLQ = %d, quero 5 (limite) ou 6", entregas)
	}
	if d := receber(t, ch, "processamento.dlq"); string(d.Body) != "job" {
		t.Errorf("DLQ: corpo = %s", d.Body)
	}
}

func TestPublicador_MensagemSemRotaDaErro(t *testing.T) {
	url := integracao.RabbitMQ(t)
	pub := mensageria.NovoPublicador(url)
	t.Cleanup(pub.Fechar)
	err := pub.Publicar(t.Context(), "", "fila-que-nao-existe", "1", []byte("x"))
	if !errors.Is(err, mensageria.ErrSemRota) {
		t.Fatalf("erro = %v, quero ErrSemRota (mandatory)", err)
	}
}
