package api_test

import (
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucazDenadai/projeto-fiapx/internal/api"
)

// encerrar põe o Vídeo num status terminal. É o papel do Worker, que ainda
// não existe; por isso o teste escreve direto no banco.
func encerrar(t *testing.T, pool *pgxpool.Pool, id, status string) {
	t.Helper()
	var motivo *string
	if status == "falhou" {
		m := "erro_interno"
		motivo = &m
	}
	if _, err := pool.Exec(t.Context(), `UPDATE videos SET status = $2, motivo = $3 WHERE id = $1`, id, status, motivo); err != nil {
		t.Fatalf("encerrar %s: %v", id, err)
	}
}

func TestCota_SextoEnvioDa429SemArquivoNovo(t *testing.T) {
	c, _ := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	for i := range 5 {
		c.enviarVideo(ana, "v"+strconv.Itoa(i)+".mp4")
	}

	r := c.enviar(ana, arquivo{"video", "sexto.mp4", mp4(4096)})
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, quero 429 (corpo %v)", r.status, r.corpo)
	}
	if arqs := c.arquivosNoVolume(); len(arqs) != 5 {
		t.Errorf("arquivos no volume = %d, quero 5", len(arqs))
	}
	if ids, _, _ := c.listar(ana, "/videos"); len(ids) != 5 {
		t.Errorf("Vídeos = %d, quero 5", len(ids))
	}
}

func TestCota_VideosTerminaisNaoContam(t *testing.T) {
	c, pool := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	terminais := []string{"concluido", "falhou"}
	for i := range 5 {
		id := c.enviarVideo(ana, "v"+strconv.Itoa(i)+".mp4")
		encerrar(t, pool, id, terminais[i%2])
	}

	if r := c.enviar(ana, arquivo{"video", "novo.mp4", mp4(4096)}); r.status != http.StatusAccepted {
		t.Fatalf("status = %d, quero 202 (corpo %v)", r.status, r.corpo)
	}
}

func TestCota_ConfiguravelEPorUsuario(t *testing.T) {
	c, _ := subirAPI(t, func(cfg *api.Config) { cfg.CotaVideos = 1 })
	ana, beto := c.usuario("ana@ex.com"), c.usuario("beto@ex.com")
	c.enviarVideo(ana, "a.mp4")

	if r := c.enviar(ana, arquivo{"video", "b.mp4", mp4(4096)}); r.status != http.StatusTooManyRequests {
		t.Errorf("Ana: status = %d, quero 429", r.status)
	}
	c.enviarVideo(beto, "c.mp4") // a cota de Ana não afeta Beto
}

// corpoRetido só entrega o conteúdo depois que liberar fecha.
type corpoRetido struct {
	liberar <-chan struct{}
	io.Reader
}

func (c corpoRetido) Read(p []byte) (int, error) {
	<-c.liberar
	return c.Reader.Read(p)
}

// Os três envios passam juntos pela checagem barata e só então mandam o
// corpo: quem decide é a checagem dentro da transação. O servidor pede o
// corpo (100 Continue) só depois da checagem barata, o que serve de barreira.
func TestCota_EnviosSimultaneosSoUmPassa(t *testing.T) {
	c, _ := subirAPI(t)
	ana := c.usuario("ana@ex.com")
	for i := range 4 {
		c.enviarVideo(ana, "v"+strconv.Itoa(i)+".mp4")
	}

	transporte := c.srv.Client().Transport.(*http.Transport).Clone()
	transporte.ExpectContinueTimeout = time.Minute
	cli := &http.Client{Transport: transporte}

	const envios = 3
	var passaram sync.WaitGroup
	passaram.Add(envios)
	liberar := make(chan struct{})
	go func() { passaram.Wait(); close(liberar) }()

	var wg sync.WaitGroup
	status := make([]int, envios)
	erros := make([]error, envios)
	for i := range envios {
		wg.Go(func() {
			var umaVez sync.Once
			passou := func() { umaVez.Do(passaram.Done) }
			defer passou() // se o servidor responder sem pedir o corpo
			ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{Got100Continue: passou})

			tipo, conteudo := multipartDe(arquivo{"video", "junto.mp4", mp4(4096)})
			req, err := novoEnvio(ctx, c.srv.URL, ana, tipo, corpoRetido{liberar, conteudo})
			if err != nil {
				erros[i] = err
				return
			}
			req.Header.Set("Expect", "100-continue")
			var r resposta
			r, erros[i] = executar(cli, req)
			status[i] = r.status
		})
	}
	wg.Wait()

	contagem := map[int]int{}
	for i, s := range status {
		if erros[i] != nil {
			t.Fatalf("envio %d: %v", i, erros[i])
		}
		contagem[s]++
	}
	if contagem[http.StatusAccepted] != 1 || contagem[http.StatusTooManyRequests] != envios-1 {
		t.Errorf("status = %v, quero um 202 e dois 429", status)
	}
	if arqs := c.arquivosNoVolume(); len(arqs) != 5 {
		t.Errorf("arquivos no volume = %d, quero 5", len(arqs))
	}
}
