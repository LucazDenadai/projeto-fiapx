package mensageria_test

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
)

// consumir roda o consumidor em segundo plano até o fim do teste.
func consumir(t *testing.T, url string, prefetch int, tratar mensageria.Tratador) {
	t.Helper()
	ctx, cancelar := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		mensageria.Consumir(ctx, url, mensageria.ConfigConsumo{
			Fila:       mensageria.FilaProcessamento,
			Prefetch:   prefetch,
			BackoffMax: 100 * time.Millisecond,
		}, tratar, slog.New(slog.DiscardHandler))
	})
	t.Cleanup(func() {
		cancelar()
		wg.Wait()
	})
}

func publicarJob(t *testing.T, ch *amqp.Channel, corpo string) {
	t.Helper()
	if err := ch.PublishWithContext(t.Context(), "", mensageria.FilaProcessamento, true, false,
		amqp.Publishing{Body: []byte(corpo)}); err != nil {
		t.Fatalf("publicar: %v", err)
	}
}

func esperar(t *testing.T, cond func() bool, oque string) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("tempo esgotado esperando %s", oque)
}

// filaDeclarada espera o consumidor declarar a topologia. A checagem passiva
// derruba o canal quando a fila não existe; por isso um canal por tentativa.
func filaDeclarada(t *testing.T, url string) {
	t.Helper()
	esperar(t, func() bool {
		_, err := canal(t, url).QueueDeclarePassive(mensageria.FilaProcessamento, true, false, false, false, nil)
		return err == nil
	}, "a fila processamento existir")
}

// mensagens conta prontas + não confirmadas na fila.
func mensagens(t *testing.T, ch *amqp.Channel, fila string) int {
	t.Helper()
	q, err := ch.QueueDeclarePassive(fila, true, false, false, false, nil)
	if err != nil {
		t.Fatalf("inspecionar %s: %v", fila, err)
	}
	return q.Messages
}

func TestConsumir_DeclaraTopologiaEConfirmaMensagem(t *testing.T) {
	url := integracao.RabbitMQ(t)
	recebidas := make(chan string, 1)
	// Vhost novo, sem fila: o consumidor declara a topologia antes do Consume.
	consumir(t, url, 1, func(_ context.Context, corpo []byte) mensageria.Resultado {
		recebidas <- string(corpo)
		return mensageria.Confirmar
	})
	ch := canal(t, url)
	filaDeclarada(t, url)

	publicarJob(t, ch, "job-1")
	select {
	case c := <-recebidas:
		if c != "job-1" {
			t.Errorf("corpo = %s", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("consumidor não recebeu a mensagem")
	}
	esperar(t, func() bool { return mensagens(t, ch, mensageria.FilaProcessamento) == 0 }, "o ack esvaziar a fila")
}

func TestConsumir_DevolverReentregaAMensagem(t *testing.T) {
	url := integracao.RabbitMQ(t)
	var entregas atomic.Int32
	consumir(t, url, 1, func(context.Context, []byte) mensageria.Resultado {
		if entregas.Add(1) == 1 {
			return mensageria.Devolver
		}
		return mensageria.Confirmar
	})
	ch := canal(t, url)
	filaDeclarada(t, url)

	publicarJob(t, ch, "job")
	esperar(t, func() bool { return entregas.Load() == 2 }, "a reentrega")
	esperar(t, func() bool { return mensagens(t, ch, mensageria.FilaProcessamento) == 0 }, "o ack esvaziar a fila")
}

func TestConsumir_PrefetchLimitaMensagensEmAndamento(t *testing.T) {
	url := integracao.RabbitMQ(t)
	var emAndamento, maximo atomic.Int32
	liberar := make(chan struct{})
	consumir(t, url, 2, func(context.Context, []byte) mensageria.Resultado {
		n := emAndamento.Add(1)
		for {
			m := maximo.Load()
			if n <= m || maximo.CompareAndSwap(m, n) {
				break
			}
		}
		<-liberar
		emAndamento.Add(-1)
		return mensageria.Confirmar
	})
	ch := canal(t, url)
	filaDeclarada(t, url)

	for range 4 {
		publicarJob(t, ch, "job")
	}
	esperar(t, func() bool { return emAndamento.Load() == 2 }, "duas mensagens em andamento ao mesmo tempo")
	time.Sleep(300 * time.Millisecond) // daria tempo de uma terceira chegar
	if m := maximo.Load(); m != 2 {
		t.Errorf("máximo em andamento = %d, quero 2 (prefetch)", m)
	}
	close(liberar)
	esperar(t, func() bool { return mensagens(t, ch, mensageria.FilaProcessamento) == 0 }, "todas confirmadas")
}

func TestConsumir_ReconectaDepoisDeQueda(t *testing.T) {
	url := integracao.RabbitMQ(t)
	proxy, urlProxy := integracao.NovoProxy(t, url)
	recebidas := make(chan string, 2)
	consumir(t, urlProxy, 1, func(_ context.Context, corpo []byte) mensageria.Resultado {
		recebidas <- string(corpo)
		return mensageria.Confirmar
	})
	ch := canal(t, url)
	filaDeclarada(t, url)

	proxy.Cortar()
	time.Sleep(300 * time.Millisecond) // o consumidor percebe a queda e tenta de novo
	proxy.Religar()
	publicarJob(t, ch, "depois-da-queda")
	select {
	case c := <-recebidas:
		if c != "depois-da-queda" {
			t.Errorf("corpo = %s", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("consumidor não voltou depois da queda")
	}
}
