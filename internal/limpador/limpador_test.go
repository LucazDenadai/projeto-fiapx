package limpador_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/banco"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/limpador"
)

func TestMain(m *testing.M) { integracao.Principal(m) }

// agora é o relógio injetado: longe do real, para nenhum teste passar por
// acaso com time.Now.
var agora = time.Date(2030, 5, 10, 12, 0, 0, 0, time.UTC)

// em devolve o instante a d do relógio injetado.
func em(d time.Duration) *time.Time {
	t := agora.Add(d)
	return &t
}

// cenario é um Worker de teste: banco e volume próprios.
type cenario struct {
	t    *testing.T
	pool *pgxpool.Pool
	url  string
	raiz string
}

func novoCenario(t *testing.T) cenario {
	t.Helper()
	pool, url := integracao.PostgresComURL(t)
	return cenario{t: t, pool: pool, url: url, raiz: t.TempDir()}
}

func (c cenario) volume() *armazenamento.Volume { return armazenamento.NovoVolume(c.raiz) }

func (c cenario) limpador(arqs limpador.Armazenamento, cfg limpador.Config) *limpador.Limpador {
	cfg.Agora = func() time.Time { return agora }
	return limpador.Novo(c.pool, arqs, cfg, slog.New(slog.DiscardHandler))
}

// rodada roda o limpador com os prazos padrão, como uma réplica nova: com o pool
// dela, porque o lock é por sessão e a mesma conexão o pegaria de novo.
func (c cenario) rodada() limpador.Resultado {
	c.t.Helper()
	replica := c
	var err error
	if replica.pool, err = banco.Abrir(c.t.Context(), c.url); err != nil {
		c.t.Fatal(err)
	}
	defer replica.pool.Close()
	r, err := replica.limpador(c.volume(), limpador.Config{}).Rodada(c.t.Context())
	if err != nil {
		c.t.Fatalf("rodada: %v", err)
	}
	if !r.Executada {
		c.t.Fatal("rodada pulada sem outra réplica rodando")
	}
	return r
}

// video cria um Vídeo no status dado; expira nil deixa o zip_expira_em vazio.
func (c cenario) video(status string, expira *time.Time) uuid.UUID {
	c.t.Helper()
	var usuario uuid.UUID
	if err := c.pool.QueryRow(c.t.Context(), `INSERT INTO usuarios (email, senha_hash) VALUES ($1, 'x') RETURNING id`,
		uuid.NewString()+"@x.com").Scan(&usuario); err != nil {
		c.t.Fatalf("inserir usuário: %v", err)
	}
	var motivo *string
	if status == "falhou" {
		m := "video_invalido"
		motivo = &m
	}
	id := uuid.New()
	if _, err := c.pool.Exec(c.t.Context(), `INSERT INTO videos (id, usuario_id, nome_original, status, motivo, zip_expira_em)
		VALUES ($1, $2, 'ferias.mp4', $3, $4, $5)`, id, usuario, status, motivo, expira); err != nil {
		c.t.Fatalf("inserir vídeo: %v", err)
	}
	return id
}

func (c cenario) dir(id uuid.UUID) string {
	return filepath.Join(c.raiz, "videos", id.String())
}

// arquivo grava um arquivo com a idade dada, e devolve o caminho. O diretório
// dele fica com a mesma idade.
func (c cenario) arquivo(caminho string, idade time.Duration) string {
	c.t.Helper()
	if err := os.MkdirAll(filepath.Dir(caminho), 0o750); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(caminho, []byte("x"), 0o600); err != nil {
		c.t.Fatal(err)
	}
	c.envelhecer(caminho, idade)
	c.envelhecer(filepath.Dir(caminho), idade)
	return caminho
}

func (c cenario) envelhecer(caminho string, idade time.Duration) {
	c.t.Helper()
	if err := os.Chtimes(caminho, *em(-idade), *em(-idade)); err != nil {
		c.t.Fatal(err)
	}
}

func (c cenario) existe(caminho string, quero bool) {
	c.t.Helper()
	_, err := os.Stat(caminho)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		c.t.Fatal(err)
	}
	if (err == nil) != quero {
		c.t.Errorf("%s: existe = %v, quero %v", caminho, err == nil, quero)
	}
}

func TestRodada_ApagaSoOZipVencidoEMantemOVideo(t *testing.T) {
	c := novoCenario(t)
	vencido, noInstante, valendo := c.video("concluido", em(-time.Second)), c.video("concluido", em(0)), c.video("concluido", em(time.Second))
	zip := func(id uuid.UUID) string { return c.arquivo(filepath.Join(c.dir(id), "frames.zip"), 8*24*time.Hour) }
	zipVencido, zipNoInstante, zipValendo := zip(vencido), zip(noInstante), zip(valendo)

	r := c.rodada()

	c.existe(zipVencido, false)
	c.existe(zipNoInstante, false) // nesse instante a API já não o entrega
	c.existe(zipValendo, true)
	if r.Zips != 2 {
		t.Errorf("zips apagados = %d, quero 2", r.Zips)
	}
	c.existe(c.dir(vencido), false) // vazio, não serve para mais nada
	var n int
	if err := c.pool.QueryRow(t.Context(), `SELECT count(*) FROM videos`).Scan(&n); err != nil || n != 3 {
		t.Errorf("vídeos no banco = %d (%v), quero os 3", n, err)
	}
	if r := c.rodada(); r.Zips != 0 {
		t.Errorf("segunda rodada apagou %d zips, quero 0", r.Zips)
	}
}

// O zip saiu, mas o Vídeo falhou antes de concluir (Postgres fora até esgotar as
// entregas): ninguém pode baixá-lo, e ele não tem prazo para vencer.
func TestRodada_ApagaZipDeVideoQueFalhou(t *testing.T) {
	c := novoCenario(t)
	falhou, processando := c.video("falhou", nil), c.video("processando", nil)
	zipFalhou := c.arquivo(filepath.Join(c.dir(falhou), "frames.zip"), time.Minute)
	zipProcessando := c.arquivo(filepath.Join(c.dir(processando), "frames.zip"), 48*time.Hour)

	r := c.rodada()

	c.existe(zipFalhou, false)
	c.existe(zipProcessando, true) // publicado, falta só concluir
	if r.Zips != 1 {
		t.Errorf("zips apagados = %d, quero 1", r.Zips)
	}
}

func TestRodada_ApagaOriginalSoDeVideoTerminal(t *testing.T) {
	c := novoCenario(t)
	original := func(status string, expira *time.Time) string {
		return c.arquivo(filepath.Join(c.dir(c.video(status, expira)), "original"), 48*time.Hour)
	}
	concluido, falhou := original("concluido", em(time.Hour)), original("falhou", nil)
	recebido, processando := original("recebido", nil), original("processando", nil)

	r := c.rodada()

	c.existe(concluido, false)
	c.existe(falhou, false)
	c.existe(recebido, true)
	c.existe(processando, true)
	if r.Originais != 2 {
		t.Errorf("originais apagados = %d, quero 2", r.Originais)
	}
}

func TestRodada_RecolheDiretoriosOrfaosComMaisDeUmaHora(t *testing.T) {
	c := novoCenario(t)
	velho, novo := c.dir(uuid.New()), c.dir(uuid.New())
	c.arquivo(filepath.Join(velho, "original"), 61*time.Minute)
	c.arquivo(filepath.Join(novo, "original"), 59*time.Minute)
	// Só um temporário velho: vai junto com o diretório, na mesma rodada.
	soTemporario := c.dir(uuid.New())
	c.arquivo(filepath.Join(soTemporario, ".original-1"), 2*time.Hour)
	// Envio em andamento: o diretório é velho, mas o temporário ainda cresce.
	emEnvio := c.dir(uuid.New())
	temporario := c.arquivo(filepath.Join(emEnvio, ".original-2"), 0)
	c.envelhecer(emEnvio, 2*time.Hour)
	// O que não é diretório de Vídeo não é do limpador.
	alheio := c.arquivo(filepath.Join(c.raiz, "videos", "lost+found", "x"), 48*time.Hour)

	r := c.rodada()

	c.existe(velho, false)
	c.existe(soTemporario, false)
	c.existe(novo, true)
	c.existe(temporario, true)
	c.existe(alheio, true)
	if r.Orfaos != 2 {
		t.Errorf("órfãos apagados = %d, quero 2", r.Orfaos)
	}
}

func TestRodada_RecolheTemporariosComMaisDeUmaHora(t *testing.T) {
	c := novoCenario(t)
	dir := c.dir(c.video("processando", nil))
	original := c.arquivo(filepath.Join(dir, "original"), 48*time.Hour)
	velhos := []string{
		c.arquivo(filepath.Join(dir, ".frames.zip-1"), 61*time.Minute),
		c.arquivo(filepath.Join(dir, ".original-1"), 61*time.Minute),
		c.arquivo(filepath.Join(c.raiz, ".readyz-1"), 61*time.Minute),
	}
	novos := []string{
		c.arquivo(filepath.Join(dir, ".frames.zip-2"), 59*time.Minute),
		c.arquivo(filepath.Join(c.raiz, ".readyz-2"), 59*time.Minute),
	}

	r := c.rodada()

	for _, f := range velhos {
		c.existe(f, false)
	}
	for _, f := range append(novos, original) {
		c.existe(f, true)
	}
	if r.Temporarios != len(velhos) {
		t.Errorf("temporários apagados = %d, quero %d", r.Temporarios, len(velhos))
	}
}

func TestRodada_SemVolumeDeVideosAinda(t *testing.T) {
	if r := novoCenario(t).rodada(); r != (limpador.Resultado{Executada: true}) {
		t.Errorf("rodada num volume vazio = %+v, quero tudo zero", r)
	}
}

func TestRodada_ExpurgaOutboxPublicadaEmLotes(t *testing.T) {
	c := novoCenario(t)
	inserir := func(publicado *time.Time, n int) {
		t.Helper()
		if _, err := c.pool.Exec(t.Context(), `INSERT INTO outbox (exchange, routing_key, payload, criado_em, publicado_em)
			SELECT '', 'processamento', '{}', $1, $2 FROM generate_series(1, $3)`, em(-30*24*time.Hour), publicado, n); err != nil {
			t.Fatal(err)
		}
	}
	inserir(nil, 1)                  // pendente antiga: nunca
	inserir(em(-8*24*time.Hour), 25) // mais de dois lotes
	inserir(em(-6*24*time.Hour), 2)  // publicada há menos de 7 dias
	cfg := limpador.Config{LoteOutbox: 10}

	r, err := c.limpador(c.volume(), cfg).Rodada(t.Context())

	if err != nil || r.Outbox != 25 {
		t.Errorf("rodada = %+v, %v; quero 25 linhas expurgadas", r, err)
	}
	var pendentes, publicadas int
	if err := c.pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE publicado_em IS NULL), count(publicado_em) FROM outbox`).
		Scan(&pendentes, &publicadas); err != nil {
		t.Fatal(err)
	}
	if pendentes != 1 || publicadas != 2 {
		t.Errorf("sobraram %d pendentes e %d publicadas, quero 1 e 2", pendentes, publicadas)
	}
}

// volumeComFalha não consegue apagar o zip de um Vídeo.
type volumeComFalha struct {
	*armazenamento.Volume
	id uuid.UUID
}

func (v volumeComFalha) ApagarZip(ctx context.Context, id uuid.UUID) error {
	if id == v.id {
		return errors.New("erro de I/O")
	}
	return v.Volume.ApagarZip(ctx, id)
}

func TestRodada_FalhaNumItemNaoImpedeOsOutros(t *testing.T) {
	c := novoCenario(t)
	var ids []uuid.UUID
	for range 3 {
		id := c.video("concluido", em(-time.Hour))
		c.arquivo(filepath.Join(c.dir(id), "frames.zip"), 8*24*time.Hour)
		ids = append(ids, id)
	}
	if _, err := c.pool.Exec(t.Context(), `INSERT INTO outbox (exchange, routing_key, payload, publicado_em) VALUES ('', 'x', '{}', $1)`,
		em(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// O volume lista em ordem alfabética: o primeiro falha, os outros vêm depois.
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })

	r, err := c.limpador(volumeComFalha{c.volume(), ids[0]}, limpador.Config{}).Rodada(t.Context())

	if err == nil || r.Zips != 2 || r.Outbox != 1 {
		t.Errorf("rodada = %+v, %v; quero o erro do volume, 2 zips e 1 linha da outbox", r, err)
	}
	c.existe(c.dir(ids[0]), true)
	c.existe(c.dir(ids[1]), false)
	c.existe(c.dir(ids[2]), false)
}

// volumeLento segura a rodada no meio até fecharem liberar.
type volumeLento struct {
	*armazenamento.Volume
	entrou, liberar chan struct{}
}

func (c cenario) volumeLento() volumeLento {
	return volumeLento{c.volume(), make(chan struct{}), make(chan struct{})}
}

func (v volumeLento) Diretorios(ctx context.Context) ([]armazenamento.Diretorio, error) {
	close(v.entrou)
	<-v.liberar
	return v.Volume.Diretorios(ctx)
}

func TestRodada_UmaReplicaPorVez(t *testing.T) {
	c := novoCenario(t)
	zip := c.arquivo(filepath.Join(c.dir(c.video("concluido", em(-time.Hour))), "frames.zip"), 8*24*time.Hour)
	lento := c.volumeLento()
	type fim struct {
		r   limpador.Resultado
		err error
	}
	primeira := make(chan fim, 1)
	go func() {
		r, err := c.limpador(lento, limpador.Config{}).Rodada(t.Context())
		primeira <- fim{r, err}
	}()
	<-lento.entrou

	segunda, err := c.limpador(c.volume(), limpador.Config{}).Rodada(t.Context())

	if err != nil || segunda.Executada {
		t.Errorf("segunda réplica = %+v, %v; quero a rodada pulada sem erro", segunda, err)
	}
	c.existe(zip, true)
	close(lento.liberar)
	if f := <-primeira; f.err != nil || f.r.Zips != 1 {
		t.Errorf("primeira réplica = %+v, quero 1 zip apagado", f)
	}
	c.rodada() // falha se o lock ficou preso
}

func TestRodada_InterrompidaLiberaOLock(t *testing.T) {
	c := novoCenario(t)
	lento := c.volumeLento()
	ctx, cancelar := context.WithCancel(t.Context())
	fim := make(chan error, 1)
	go func() {
		_, err := c.limpador(lento, limpador.Config{}).Rodada(ctx)
		fim <- err
	}()
	<-lento.entrou
	cancelar()
	close(lento.liberar)
	if err := <-fim; !errors.Is(err, context.Canceled) {
		t.Errorf("erro = %v, quero context.Canceled", err)
	}

	c.rodada() // falha se o lock ficou preso
}

func TestIniciar_RodaNaSubidaERepete(t *testing.T) {
	c := novoCenario(t)
	zip := func() string {
		return c.arquivo(filepath.Join(c.dir(c.video("concluido", em(-time.Hour))), "frames.zip"), 8*24*time.Hour)
	}
	esperarSumir := func(caminho string) {
		t.Helper()
		for limite := time.Now().Add(5 * time.Second); time.Now().Before(limite); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(caminho); errors.Is(err, os.ErrNotExist) {
				return
			}
		}
		t.Fatalf("%s não foi apagado", caminho)
	}
	primeiro := zip()
	ctx, cancelar := context.WithCancel(t.Context())

	esperar := c.limpador(c.volume(), limpador.Config{Intervalo: 20 * time.Millisecond}).Iniciar(ctx)
	defer func() { cancelar(); esperar() }()

	esperarSumir(primeiro)
	// O limpador só vê diretório com zip: o arquivo é criado inteiro antes.
	segundo := c.video("concluido", em(-time.Hour))
	pronto := filepath.Join(c.raiz, "pronto")
	if err := os.Mkdir(pronto, 0o750); err != nil {
		t.Fatal(err)
	}
	c.arquivo(filepath.Join(pronto, "frames.zip"), 8*24*time.Hour)
	if err := os.Rename(pronto, c.dir(segundo)); err != nil {
		t.Fatal(err)
	}
	esperarSumir(c.dir(segundo))
}

func TestMetricas_ItensPorTipoEDuracao(t *testing.T) {
	c := novoCenario(t)
	reg := prometheus.NewRegistry()
	c.arquivo(filepath.Join(c.dir(c.video("concluido", em(-time.Hour))), "frames.zip"), 8*24*time.Hour)
	c.arquivo(filepath.Join(c.dir(uuid.New()), "original"), 2*time.Hour)
	l := c.limpador(c.volume(), limpador.Config{Registro: reg})

	for range 2 {
		if _, err := l.Rodada(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	if err := testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP fiapx_limpador_itens_total Itens apagados pelo limpador, por tipo.
# TYPE fiapx_limpador_itens_total counter
fiapx_limpador_itens_total{tipo="orfao"} 1
fiapx_limpador_itens_total{tipo="original"} 0
fiapx_limpador_itens_total{tipo="outbox"} 0
fiapx_limpador_itens_total{tipo="temporario"} 0
fiapx_limpador_itens_total{tipo="zip"} 1
`), "fiapx_limpador_itens_total"); err != nil {
		t.Error(err)
	}
	if n, _ := integracao.Metrica(t, reg, "fiapx_limpador_rodada_segundos"); n != 2 {
		t.Errorf("rodadas medidas = %v, quero 2", n)
	}
}
