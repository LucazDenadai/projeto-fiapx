package notificacao_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/quotedprintable"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/notificacao"
	"github.com/LucazDenadai/projeto-fiapx/internal/retentativa"
)

func TestMain(m *testing.M) { integracao.Principal(m) }

func decodificarQP(t *testing.T, b []byte) string {
	t.Helper()
	s, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(b)))
	if err != nil {
		t.Fatalf("quoted-printable: %v", err)
	}
	return string(s)
}

// notifier roda o consumidor da fila notificacao até o fim do teste.
func notifier(t *testing.T, amqpURL, smtp string, top mensageria.Topologia, retry retentativa.Politica) {
	t.Helper()
	n := notificacao.NovoNotificador(notificacao.NovoSMTP(notificacao.ConfigSMTP{Endereco: smtp}),
		notificacao.Config{Remetente: "nao-responda@fiapx.local", Retry: retry}, slog.New(slog.DiscardHandler))
	ctx, cancelar := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		mensageria.Consumir(ctx, amqpURL, mensageria.ConfigConsumo{Fila: mensageria.FilaNotificacao, Topologia: top},
			n.Notificar, slog.New(slog.DiscardHandler))
	})
	t.Cleanup(func() { cancelar(); wg.Wait() })
}

// publicarEvento espera o Notifier consumir (a topologia inteira já foi
// declarada: o Consume vem depois da exchange e da ligação) e publica o
// evento na exchange de eventos do Processamento, como o relay do Worker faz.
func publicarEvento(t *testing.T, url, evento string, campos map[string]any) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var ch *amqp.Channel
	consumindo := false
	for range 100 {
		if ch, err = conn.Channel(); err != nil {
			t.Fatal(err)
		}
		q, err := ch.QueueDeclarePassive(mensageria.FilaNotificacao, true, false, false, false, nil)
		if consumindo = err == nil && q.Consumers > 0; consumindo {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !consumindo {
		t.Fatal("o Notifier não começou a consumir a fila notificacao")
	}
	corpo, _ := json.Marshal(campos)
	if err := ch.PublishWithContext(t.Context(), mensageria.ExchangeEventos, evento, false, false,
		amqp.Publishing{ContentType: "application/json", Body: corpo}); err != nil {
		t.Fatalf("publicar %s: %v", evento, err)
	}
	return ch
}

func eventoFalhou(email string) map[string]any {
	return map[string]any{
		"video_id":       uuid.NewString(),
		"correlation_id": "corr-" + uuid.NewString(),
		"email":          email,
		"nome_original":  "ferias.mp4",
		"motivo":         "video_muito_longo",
		"ocorrido_em":    time.Date(2026, 11, 24, 12, 30, 0, 0, time.UTC),
	}
}

func TestNotifier_EmailDeFalhaChegaNoMailpit(t *testing.T) {
	mp := integracao.NovoMailpit(t)
	url := integracao.RabbitMQ(t)
	notifier(t, url, mp.SMTP, mensageria.Topologia{}, retentativa.Politica{})
	ana := "ana-" + uuid.NewString()[:8] + "@ex.com"
	ev := eventoFalhou(ana)

	publicarEvento(t, url, mensageria.EventoFalhou, ev)

	emails := mp.EsperarEmailPara(ana, 15*time.Second)
	if len(emails) != 1 {
		t.Fatalf("e-mails para %s = %d, quero 1", ana, len(emails))
	}
	e := emails[0]
	if e.Assunto != "Não conseguimos processar seu vídeo" || e.HTML != "" {
		t.Errorf("assunto %q, html %q", e.Assunto, e.HTML)
	}
	for _, quero := range []string{"ferias.mp4", "24/11/2026 às 09:30", "o vídeo passa da duração máxima", ev["video_id"].(string)} {
		if !strings.Contains(e.Texto, quero) {
			t.Errorf("texto sem %q:\n%s", quero, e.Texto)
		}
	}
	if strings.Contains(e.Texto, ev["correlation_id"].(string)) {
		t.Errorf("texto vaza o correlation_id:\n%s", e.Texto)
	}
}

// O evento concluido não é roteado para a fila notificacao (ADR-0008).
func TestNotifier_SucessoNaoNotifica(t *testing.T) {
	mp := integracao.NovoMailpit(t)
	url := integracao.RabbitMQ(t)
	notifier(t, url, mp.SMTP, mensageria.Topologia{}, retentativa.Politica{})
	beto := "beto-" + uuid.NewString()[:8] + "@ex.com"

	ch := publicarEvento(t, url, mensageria.EventoConcluido, eventoFalhou(beto))
	// Um falhou depois, de outro Usuário, prova que o Notifier estava
	// consumindo: quando ele chega, o concluido já teria chegado.
	controle := "controle-" + uuid.NewString()[:8] + "@ex.com"
	publicarEvento(t, url, mensageria.EventoFalhou, eventoFalhou(controle))

	if len(mp.EsperarEmailPara(controle, 15*time.Second)) != 1 {
		t.Fatal("o e-mail de controle não chegou")
	}
	if n := len(mp.EmailsPara(beto)); n != 0 {
		t.Errorf("e-mails para %s = %d, quero 0", beto, n)
	}
	q, err := ch.QueueDeclarePassive(mensageria.FilaNotificacao, true, false, false, false, nil)
	if err != nil || q.Messages != 0 {
		t.Errorf("notificacao com %d mensagens (%v), quero 0", q.Messages, err)
	}
}

// SMTP fora: backoff no processo, nack, delivery-limit e, passado ele, a
// mensagem fica parada na notificacao.dlq (ADR-0004).
func TestNotifier_SMTPForaTerminaNaDLQ(t *testing.T) {
	mp := integracao.NovoMailpit(t)
	proxy, smtpCortado := integracao.NovoProxy(t, "smtp://"+mp.SMTP)
	proxy.Cortar()
	url := integracao.RabbitMQ(t)
	notifier(t, url, strings.TrimPrefix(smtpCortado, "smtp://"), mensageria.Topologia{LimiteEntregas: 2},
		retentativa.Politica{Inicial: 50 * time.Millisecond, Orcamento: 200 * time.Millisecond})
	carla := "carla-" + uuid.NewString()[:8] + "@ex.com"

	ch := publicarEvento(t, url, mensageria.EventoFalhou, eventoFalhou(carla))

	dlq := mensageria.DLQ(mensageria.FilaNotificacao)
	for range 100 {
		if q, err := ch.QueueDeclarePassive(dlq, true, false, false, false, nil); err == nil && q.Messages == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	q, err := ch.QueueDeclarePassive(dlq, true, false, false, false, nil)
	if err != nil || q.Messages != 1 || q.Consumers != 0 {
		t.Fatalf("%s: %d mensagens, %d consumidores (%v); quero 1 mensagem parada", dlq, q.Messages, q.Consumers, err)
	}
	proxy.Religar()
	if n := len(mp.EmailsPara(carla)); n != 0 {
		t.Errorf("e-mails para %s = %d, quero 0", carla, n)
	}
}

// O Notifier não acessa o Postgres (ADR-0001, ADR-0008): nem o driver entra
// no binário. Assim o status do Vídeo não muda por causa da notificação.
func TestNotifier_BinarioSemPostgres(t *testing.T) {
	saida, err := exec.CommandContext(t.Context(), "go", "list", "-deps", "../../cmd/notifier").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, proibido := range []string{"github.com/jackc/pgx", "internal/banco", "internal/videos"} {
		if strings.Contains(string(saida), proibido) {
			t.Errorf("cmd/notifier depende de %s", proibido)
		}
	}
}
