package processamento_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
	"github.com/LucazDenadai/projeto-fiapx/internal/extracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/processamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/retentativa"
)

// novoCenarioComProxy é o cenário com um pool que passa por um Proxy, para
// derrubar o Postgres no meio do Processamento.
func novoCenarioComProxy(t *testing.T) (cenario, *pgxpool.Pool, *integracao.Proxy) {
	t.Helper()
	pool, url := integracao.PostgresComURL(t)
	proxy, urlProxy := integracao.NovoProxy(t, url)
	viaProxy, err := banco.Abrir(t.Context(), urlProxy)
	if err != nil {
		t.Fatalf("pool pelo proxy: %v", err)
	}
	t.Cleanup(viaProxy.Close)
	c := cenario{pool: pool, volume: armazenamento.NovoVolume(t.TempDir()), trabalho: t.TempDir()}
	return c, viaProxy, proxy
}

// derrubaNaExtracao corta a dependência atrás do proxy enquanto o ffmpeg roda
// e a religa depois de fora.
type derrubaNaExtracao struct {
	processamento.Extrator
	proxy *integracao.Proxy
	fora  time.Duration
}

func (d derrubaNaExtracao) ExtrairFrames(ctx context.Context, arquivo, dir string) ([]string, error) {
	d.proxy.Cortar()
	time.AfterFunc(d.fora, d.proxy.Religar)
	return d.Extrator.ExtrairFrames(ctx, arquivo, dir)
}

func TestProcessar_QuedaBreveDoPostgresNaoFalhaOVideo(t *testing.T) {
	c, viaProxy, proxy := novoCenarioComProxy(t)
	id := c.videoRecebido(t, "recebido", 2*time.Second)
	ext := derrubaNaExtracao{Extrator: extracao.Novo(extracao.Config{}), proxy: proxy, fora: 10 * time.Second}
	// Orçamento e backoff padrão (~1 min; 1, 2, 4, 8 s…).
	proc := processamento.NovoProcessador(viaProxy, c.volume, ext,
		processamento.Config{DirTrabalho: c.trabalho}, slog.New(slog.DiscardHandler))

	if r := proc.Processar(t.Context(), mensagem(id, "c")); r != mensageria.Confirmar {
		t.Fatalf("resultado = %v, quero Confirmar", r)
	}
	if s := c.status(t, id); s != "concluido" {
		t.Errorf("status = %s, quero concluido", s)
	}
	if n := len(c.eventos(t)); n != 1 {
		t.Errorf("eventos = %d, quero 1 (concluido)", n)
	}
}

func TestProcessar_OrcamentoEsgotadoDevolve(t *testing.T) {
	c, viaProxy, proxy := novoCenarioComProxy(t)
	id := c.videoRecebido(t, "recebido", time.Second)
	ext := derrubaNaExtracao{Extrator: extracao.Novo(extracao.Config{}), proxy: proxy, fora: time.Hour}
	proc := processamento.NovoProcessador(viaProxy, c.volume, ext, processamento.Config{
		DirTrabalho: c.trabalho,
		Retry:       retentativa.Politica{Inicial: 100 * time.Millisecond, Orcamento: time.Second},
	}, slog.New(slog.DiscardHandler))

	inicio := time.Now()
	r := proc.Processar(t.Context(), mensagem(id, "c"))

	if r != mensageria.Devolver {
		t.Errorf("resultado = %v, quero Devolver", r)
	}
	// O orçamento é usado inteiro: a última espera encolhe para caber nele.
	if d := time.Since(inicio); d < 900*time.Millisecond || d > 5*time.Second {
		t.Errorf("desistiu em %v; o orçamento era 1 s", d)
	}
	if _, err := os.Stat(c.volume.Original(id)); err != nil {
		t.Errorf("original sumiu: %v", err)
	}
}

func TestProcessarDLQ(t *testing.T) {
	casos := []struct {
		nome, status, quero string
		eventos             int
	}{
		{"recebido falha com erro_interno", "recebido", "falhou", 1},
		{"processando falha com erro_interno", "processando", "falhou", 1},
		{"concluido só confirma", "concluido", "concluido", 0},
		{"falhou só confirma", "falhou", "falhou", 0},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			c := novoCenario(t)
			id := c.videoRecebido(t, "recebido", time.Second)
			motivo := any(nil)
			if tc.status == "falhou" {
				motivo = "sem_frames"
			}
			if _, err := c.pool.Exec(t.Context(), `UPDATE videos SET status = $2, motivo = $3 WHERE id = $1`,
				id, tc.status, motivo); err != nil {
				t.Fatal(err)
			}

			if r := c.processador(t, nil, nil).ProcessarDLQ(t.Context(), mensagem(id, "corr-dlq")); r != mensageria.Confirmar {
				t.Fatalf("resultado = %v, quero Confirmar", r)
			}
			if s := c.status(t, id); s != tc.quero {
				t.Errorf("status = %s, quero %s", s, tc.quero)
			}
			evs := c.eventos(t)
			if len(evs) != tc.eventos {
				t.Fatalf("eventos = %d, quero %d", len(evs), tc.eventos)
			}
			if tc.eventos == 1 {
				e := evs[0]
				if e.routingKey != "falhou" || e.payload["motivo"] != "erro_interno" || e.payload["correlation_id"] != "corr-dlq" ||
					e.payload["email"] == nil || e.payload["nome_original"] != "ferias.mp4" {
					t.Errorf("evento = %+v", e)
				}
				if _, err := os.Stat(c.volume.Original(id)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("original ainda existe: %v", err)
				}
			}
		})
	}
}

func TestProcessarDLQ_MensagemSemVideoEhDescartada(t *testing.T) {
	c := novoCenario(t)
	for nome, corpo := range map[string][]byte{
		"video inexistente": mensagem(uuid.New(), "c"),
		"json invalido":     []byte("{"),
	} {
		t.Run(nome, func(t *testing.T) {
			if r := c.processador(t, nil, nil).ProcessarDLQ(t.Context(), corpo); r != mensageria.Confirmar {
				t.Errorf("resultado = %v, quero Confirmar", r)
			}
		})
	}
}

// worker roda os dois consumidores do Worker (fila processamento, pela URL
// dada, e processamento.dlq, direto no broker) até o fim do teste.
func worker(t *testing.T, proc *processamento.Processador, urlFila, urlDLQ string, top mensageria.Topologia) {
	t.Helper()
	ctx, cancelar := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	log := slog.New(slog.DiscardHandler)
	wg.Go(func() {
		mensageria.Consumir(ctx, urlFila, mensageria.ConfigConsumo{Fila: mensageria.FilaProcessamento,
			Topologia: top, BackoffMax: 200 * time.Millisecond}, proc.Processar, log)
	})
	wg.Go(func() {
		mensageria.Consumir(ctx, urlDLQ, mensageria.ConfigConsumo{Fila: mensageria.DLQ(mensageria.FilaProcessamento),
			Topologia: top}, proc.ProcessarDLQ, log)
	})
	t.Cleanup(func() { cancelar(); wg.Wait() })
}

// publicarPedido espera a fila existir e publica o pedido de Processamento.
func publicarPedido(t *testing.T, url string, id uuid.UUID) {
	t.Helper()
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var ch *amqp.Channel
	for range 100 {
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
		amqp.Publishing{Body: mensagem(id, "corr-esgotado")}); err != nil {
		t.Fatalf("publicar pedido: %v", err)
	}
}

// esperarFalhou espera o Vídeo terminar falhou e confere o Motivo e o evento.
func (c cenario) esperarFalhou(t *testing.T, id uuid.UUID, motivo string, ate time.Duration) {
	t.Helper()
	limite := time.Now().Add(ate)
	for time.Now().Before(limite) && c.status(t, id) != "falhou" {
		time.Sleep(100 * time.Millisecond)
	}
	var status, m string
	if err := c.pool.QueryRow(t.Context(), `SELECT status, coalesce(motivo, '') FROM videos WHERE id = $1`, id).
		Scan(&status, &m); err != nil {
		t.Fatal(err)
	}
	if status != "falhou" || m != motivo {
		t.Fatalf("status = %s, motivo = %q; quero falhou, %s", status, m, motivo)
	}
	evs := c.eventos(t)
	if len(evs) != 1 || evs[0].routingKey != "falhou" || evs[0].payload["motivo"] != motivo {
		t.Errorf("eventos = %+v, quero um falhou com %s", evs, motivo)
	}
}

// semSaida é um erro transitório que nunca passa.
type semSaida struct{ processamento.Extrator }

func (semSaida) ExtrairFrames(context.Context, string, string) ([]string, error) {
	return nil, errors.New("volume instável")
}

func TestWorker_TentativasEsgotadasVaoParaDLQEFalham(t *testing.T) {
	c := novoCenario(t)
	url := integracao.RabbitMQ(t)
	id := c.videoRecebido(t, "recebido", time.Second)
	top := mensageria.Topologia{LimiteEntregas: 2}
	worker(t, c.processador(t, nil, semSaida{extracao.Novo(extracao.Config{})}), url, url, top)

	publicarPedido(t, url, id)

	c.esperarFalhou(t, id, "erro_interno", 20*time.Second)
}

// derrubaOWorker simula um Vídeo que derruba o Worker a cada tentativa: a
// conexão com o broker cai com a mensagem em mãos, sem ack nem nack.
type derrubaOWorker struct {
	processamento.Extrator
	proxy *integracao.Proxy
}

func (d derrubaOWorker) ExtrairFrames(context.Context, string, string) ([]string, error) {
	d.proxy.Cortar()
	time.AfterFunc(300*time.Millisecond, d.proxy.Religar)
	return nil, errors.New("worker morreu")
}

func TestWorker_MorrendoEmLoopTerminaFalhou(t *testing.T) {
	c := novoCenario(t)
	url := integracao.RabbitMQ(t)
	proxy, urlProxy := integracao.NovoProxy(t, url)
	id := c.videoRecebido(t, "recebido", time.Second)
	top := mensageria.Topologia{LimiteEntregas: 2}
	proc := c.processador(t, nil, derrubaOWorker{Extrator: extracao.Novo(extracao.Config{}), proxy: proxy})
	worker(t, proc, urlProxy, url, top)

	publicarPedido(t, url, id)

	c.esperarFalhou(t, id, "erro_interno", 30*time.Second)
}

// Volume fora do ar por um instante: a leitura do original tenta de novo, em
// vez de gastar as entregas em segundos (ADR-0004).
func TestProcessar_OriginalIndisponivelPorInstanteTentaDeNovo(t *testing.T) {
	c := novoCenario(t)
	id := c.videoRecebido(t, "recebido", time.Second)
	original := c.volume.Original(id)
	escondido := original + ".fora"
	if err := os.Rename(original, escondido); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(500*time.Millisecond, func() { _ = os.Rename(escondido, original) })
	proc := c.processadorCom(t, nil, nil, processamento.Config{
		Retry: retentativa.Politica{Inicial: 100 * time.Millisecond, Orcamento: 5 * time.Second},
	}, slog.New(slog.DiscardHandler))

	if r := proc.Processar(t.Context(), mensagem(id, "c")); r != mensageria.Confirmar {
		t.Fatalf("resultado = %v, quero Confirmar", r)
	}
	if s := c.status(t, id); s != "concluido" {
		t.Errorf("status = %s, quero concluido", s)
	}
}
