// Package limite conta tentativas de login e cadastro no Redis, em janela fixa,
// para dificultar força bruta (ADR-0009). Com o Redis fora do ar ou lento,
// deixa passar (fail-open) e registra o erro no log.
package limite

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

// Regra é o máximo de tentativas permitidas dentro de uma janela.
type Regra struct {
	Max    int64
	Janela time.Duration
}

// Regras reúne os limites de login e cadastro.
type Regras struct {
	LoginPorEmail Regra
	LoginPorIP    Regra
	CadastroPorIP Regra
}

// RegrasPadrao devolve os valores do ADR-0009.
func RegrasPadrao() Regras {
	return Regras{
		LoginPorEmail: Regra{Max: 5, Janela: 15 * time.Minute},
		LoginPorIP:    Regra{Max: 20, Janela: time.Minute},
		CadastroPorIP: Regra{Max: 5, Janela: time.Hour},
	}
}

// TimeoutPadrao é quanto uma chamada ao Redis pode segurar o login.
const TimeoutPadrao = 200 * time.Millisecond

// Config configura as Tentativas.
type Config struct {
	Regras
	// Prefixo vai antes de toda chave; os testes o usam para se isolar.
	Prefixo string
	// Timeout de cada chamada ao Redis; zero usa TimeoutPadrao.
	Timeout time.Duration
	// Registro recebe a métrica de falhas; nil não registra.
	Registro prometheus.Registerer
}

// Tentativas aplica as Regras de login e cadastro.
type Tentativas struct {
	redis  *redis.Client
	cfg    Config
	falhas prometheus.Counter
	log    *slog.Logger
}

// Novo monta as Tentativas com um cliente Redis próprio. O cliente respeita o
// prazo do contexto (ContextTimeoutEnabled): sem isso o go-redis ignora o
// Timeout e um Redis travado seguraria o login por segundos.
func Novo(opcoes *redis.Options, cfg Config, log *slog.Logger) *Tentativas {
	if cfg.Timeout <= 0 {
		cfg.Timeout = TimeoutPadrao
	}
	o := *opcoes
	o.ContextTimeoutEnabled = true
	falhas := promauto.With(cfg.Registro).NewCounter(prometheus.CounterOpts{
		Name: "fiapx_redis_falhas_total",
		Help: "Chamadas ao Redis do limite de tentativas que falharam (seguiu sem limite).",
	})
	return &Tentativas{redis: redis.NewClient(&o), cfg: cfg, falhas: falhas, log: log}
}

// Fechar encerra as conexões com o Redis.
func (t *Tentativas) Fechar() error {
	return t.redis.Close()
}

// Bloqueio diz se a tentativa passou do limite e quanto falta para a janela
// acabar.
type Bloqueio struct {
	Bloqueado bool
	Esperar   time.Duration
}

// Login conta a tentativa por IP e por e-mail. Toda tentativa conta, antes de
// conferir a senha, para que tentativas paralelas não passem do limite; o
// login com sucesso zera o e-mail com LoginComSucesso.
func (t *Tentativas) Login(ctx context.Context, email, ip string) Bloqueio {
	if b := t.contar(ctx, "login:ip:"+ip, t.cfg.LoginPorIP); b.Bloqueado {
		return b
	}
	chave, ok := chaveEmail(email)
	if !ok {
		return Bloqueio{} // sem e-mail não há conta a proteger nem chave a contar
	}
	return t.contar(ctx, chave, t.cfg.LoginPorEmail)
}

// LoginComSucesso zera o contador do e-mail.
func (t *Tentativas) LoginComSucesso(ctx context.Context, email string) {
	sufixo, ok := chaveEmail(email)
	if !ok {
		return
	}
	ctxPedido := ctx
	ctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	if err := t.redis.Del(ctx, t.chave(sufixo)).Err(); err != nil {
		t.contarFalha(ctxPedido)
		t.log.ErrorContext(ctx, "limite de tentativas: zerar contador no redis", "erro", err)
	}
}

// Cadastro conta a tentativa de cadastro por IP.
func (t *Tentativas) Cadastro(ctx context.Context, ip string) Bloqueio {
	return t.contar(ctx, "cadastro:ip:"+ip, t.cfg.CadastroPorIP)
}

// contarFalha conta a falha do Redis, salvo se foi o cliente que desistiu do
// pedido.
func (t *Tentativas) contarFalha(ctxPedido context.Context) {
	if ctxPedido.Err() == nil {
		t.falhas.Inc()
	}
}

func (t *Tentativas) chave(sufixo string) string {
	return t.cfg.Prefixo + "limite:" + sufixo
}

// contar soma 1 na janela fixa da chave (INCR + EXPIRE NX no mesmo pipeline).
func (t *Tentativas) contar(ctx context.Context, sufixo string, r Regra) Bloqueio {
	ctxPedido := ctx
	ctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()
	chave := t.chave(sufixo)
	var total *redis.IntCmd
	var ttl *redis.DurationCmd
	_, err := t.redis.Pipelined(ctx, func(p redis.Pipeliner) error {
		total = p.Incr(ctx, chave)
		p.ExpireNX(ctx, chave, r.Janela)
		ttl = p.PTTL(ctx, chave)
		return nil
	})
	if err != nil {
		t.contarFalha(ctxPedido)
		// Fail-open (ADR-0009): o Redis é proteção auxiliar.
		t.log.ErrorContext(ctx, "limite de tentativas: redis indisponível, seguindo sem limite", "erro", err)
		return Bloqueio{}
	}
	if total.Val() <= r.Max {
		return Bloqueio{}
	}
	esperar := ttl.Val()
	if esperar <= 0 || esperar > r.Janela {
		esperar = r.Janela
	}
	return Bloqueio{Bloqueado: true, Esperar: esperar}
}

// chaveEmail devolve a chave do contador do e-mail, normalizado como no
// cadastro (minúsculas). Falso para e-mail vazio.
func chaveEmail(email string) (string, bool) {
	email = strings.ToLower(strings.TrimSpace(email))
	return "login:email:" + email, email != ""
}
