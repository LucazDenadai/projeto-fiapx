package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/outbox"
)

func TestMain(m *testing.M) { integracao.Principal(m) }

var cfgTeste = outbox.Config{
	Intervalo:         20 * time.Millisecond,
	Lote:              10,
	BackoffMax:        50 * time.Millisecond,
	TimeoutPublicacao: 2 * time.Second,
}

func novoRelay(t *testing.T, pool *pgxpool.Pool, url string) *outbox.Relay {
	t.Helper()
	pub := mensageria.NovoPublicador(url)
	t.Cleanup(pub.Fechar)
	return outbox.NovoRelay(pool, pub, cfgTeste, slog.New(slog.DiscardHandler))
}

// inserirPendentes insere n linhas pendentes na outbox, como faz a API ao aceitar um
// Vídeo, e devolve os payloads.
func inserirPendentes(t *testing.T, pool *pgxpool.Pool, n int) []string {
	t.Helper()
	var payloads []string
	for i := range n {
		p := fmt.Sprintf(`{"video_id": "v%d", "correlation_id": "c%d"}`, i, i)
		if _, err := pool.Exec(t.Context(),
			`INSERT INTO outbox (exchange, routing_key, payload) VALUES ('', 'processamento', $1)`, p); err != nil {
			t.Fatalf("inserir na outbox: %v", err)
		}
		payloads = append(payloads, p)
	}
	return payloads
}

func pendentes(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE publicado_em IS NULL`).Scan(&n); err != nil {
		t.Fatalf("contar pendentes: %v", err)
	}
	return n
}

// drenar tira todas as mensagens da fila (espera a primeira chegar).
func drenar(t *testing.T, url, fila string) []amqp.Delivery {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("canal: %v", err)
	}
	var msgs []amqp.Delivery
	for tentativas := 0; tentativas < 30; {
		d, ok, err := ch.Get(fila, true)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !ok {
			if len(msgs) > 0 {
				break
			}
			tentativas++
			time.Sleep(100 * time.Millisecond)
			continue
		}
		msgs = append(msgs, d)
	}
	return msgs
}

func TestRelay_VideoAceitoChegaNaFilaProcessamento(t *testing.T) {
	pool, url := integracao.Postgres(t), integracao.RabbitMQ(t)
	payloads := inserirPendentes(t, pool, 1)

	if _, err := novoRelay(t, pool, url).Rodada(t.Context()); err != nil {
		t.Fatalf("rodada: %v", err)
	}

	msgs := drenar(t, url, "processamento")
	if len(msgs) != 1 {
		t.Fatalf("mensagens = %d, quero 1", len(msgs))
	}
	var corpo, quero map[string]string
	mustJSON(t, msgs[0].Body, &corpo)
	mustJSON(t, []byte(payloads[0]), &quero)
	if !maps.Equal(corpo, quero) {
		t.Errorf("corpo = %v, quero %v", corpo, quero)
	}
	if msgs[0].DeliveryMode != amqp.Persistent {
		t.Error("a mensagem deveria ser persistente")
	}
	if n := pendentes(t, pool); n != 0 {
		t.Errorf("pendentes = %d, quero 0 (publicado_em gravado)", n)
	}
}

func TestRelay_BrokerForaMantemPendenteEPublicaQuandoVolta(t *testing.T) {
	pool, url := integracao.Postgres(t), integracao.RabbitMQ(t)
	proxy, viaProxy := integracao.NovoProxy(t, url)
	relay := novoRelay(t, pool, viaProxy)
	inserirPendentes(t, pool, 3)

	proxy.Cortar()
	if _, err := relay.Rodada(t.Context()); err == nil {
		t.Fatal("rodada com o broker fora deveria falhar")
	}
	if n := pendentes(t, pool); n != 3 {
		t.Fatalf("pendentes = %d, quero 3", n)
	}

	proxy.Religar()
	ctx, cancel := context.WithCancel(t.Context())
	parado := relay.Iniciar(ctx)
	defer func() { cancel(); parado() }()
	esperar(t, func() bool { return pendentes(t, pool) == 0 })
	if msgs := drenar(t, url, "processamento"); len(msgs) != 3 {
		t.Errorf("mensagens = %d, quero 3", len(msgs))
	}
}

func TestRelay_DuasReplicasNaoDisputamLinhas(t *testing.T) {
	pool, url := integracao.Postgres(t), integracao.RabbitMQ(t)
	inserirPendentes(t, pool, 100)

	ctx, cancel := context.WithCancel(t.Context())
	var paradas []func()
	for range 2 {
		paradas = append(paradas, novoRelay(t, pool, url).Iniciar(ctx))
	}
	esperar(t, func() bool { return pendentes(t, pool) == 0 })
	cancel()
	for _, parar := range paradas {
		parar()
	}

	msgs := drenar(t, url, "processamento")
	vistos := map[string]int{}
	for _, m := range msgs {
		vistos[string(m.Body)]++
	}
	if len(msgs) != 100 || len(vistos) != 100 {
		t.Errorf("mensagens = %d, distintas = %d; quero 100 e 100", len(msgs), len(vistos))
	}
}

func TestRelay_IniciarDeclaraATopologiaMesmoSemPendentes(t *testing.T) {
	pool, url := integracao.Postgres(t), integracao.RabbitMQ(t)
	ctx, cancel := context.WithCancel(t.Context())
	parar := novoRelay(t, pool, url).Iniciar(ctx)
	defer func() { cancel(); parar() }()

	esperar(t, func() bool {
		conn, err := amqp.Dial(url)
		if err != nil {
			return false
		}
		defer func() { _ = conn.Close() }()
		ch, err := conn.Channel()
		if err != nil {
			return false
		}
		_, err = ch.QueueDeclarePassive("processamento", true, false, false, false, nil)
		return err == nil
	})
}

func esperar(t *testing.T, cond func() bool) {
	t.Helper()
	limite := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(limite) {
			t.Fatal("condição não atingida a tempo")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func mustJSON(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("json %s: %v", b, err)
	}
}

func TestRelay_LinhaSemRotaNaoTravaAsOutras(t *testing.T) {
	pool, url := integracao.Postgres(t), integracao.RabbitMQ(t)
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO outbox (exchange, routing_key, payload) VALUES ('', 'fila-que-nao-existe', '{}')`); err != nil {
		t.Fatalf("inserir: %v", err)
	}
	inserirPendentes(t, pool, 2)

	n, err := novoRelay(t, pool, url).Rodada(t.Context())
	if !errors.Is(err, mensageria.ErrSemRota) {
		t.Errorf("erro = %v, quero ErrSemRota", err)
	}
	if n != 2 || pendentes(t, pool) != 1 {
		t.Errorf("publicadas = %d, pendentes = %d; quero 2 e 1 (só a sem rota)", n, pendentes(t, pool))
	}
	if msgs := drenar(t, url, "processamento"); len(msgs) != 2 {
		t.Errorf("mensagens = %d, quero 2", len(msgs))
	}
}

// Evento é fan-out: assinar é opcional (ADR-0002). Sem fila ligada à routing
// key (hoje, concluido), o evento conta como publicado e não volta toda rodada.
func TestRelay_EventoSemAssinanteContaComoPublicado(t *testing.T) {
	pool, url := integracao.Postgres(t), integracao.RabbitMQ(t)
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO outbox (exchange, routing_key, payload) VALUES ($1, 'concluido', '{}')`,
		mensageria.ExchangeEventos); err != nil {
		t.Fatalf("inserir: %v", err)
	}

	n, err := novoRelay(t, pool, url).Rodada(t.Context())
	if err != nil {
		t.Fatalf("rodada: %v", err)
	}
	if n != 1 || pendentes(t, pool) != 0 {
		t.Errorf("publicadas = %d, pendentes = %d; quero 1 e 0", n, pendentes(t, pool))
	}
}
