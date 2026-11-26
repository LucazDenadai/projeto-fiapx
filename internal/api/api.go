// Package api monta o roteador HTTP público da API: cadastro, login e as
// rotas de Vídeo protegidas pelo JWT.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/LucazDenadai/projeto-fiapx/internal/armazenamento"
	"github.com/LucazDenadai/projeto-fiapx/internal/auth"
	"github.com/LucazDenadai/projeto-fiapx/internal/limite"
	"github.com/LucazDenadai/projeto-fiapx/internal/videos"
)

// Config reúne as dependências da API.
type Config struct {
	Pool       *pgxpool.Pool
	SegredoJWT []byte
	Log        *slog.Logger
	Tentativas *limite.Tentativas
	// DirDados é a raiz do volume de arquivos (/data no Compose).
	DirDados string
	// TamanhoMaxEnvio e TimeoutEnvio valem só para POST /videos; zero usa os
	// padrões (500 MB e 10 min).
	TamanhoMaxEnvio int64
	TimeoutEnvio    time.Duration
	// CotaVideos limita os Vídeos não terminais por Usuário (ADR-0011); zero
	// ou negativo usa o padrão (5).
	CotaVideos int
	// Registro recebe as métricas da API; nil não registra.
	Registro prometheus.Registerer
}

// prefixoVideos leva às rotas protegidas.
const prefixoVideos = "/videos"

// Novo devolve o roteador público. Falha se a configuração estiver incompleta,
// por exemplo sem o segredo do JWT.
func Novo(cfg Config) (http.Handler, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Tentativas == nil || cfg.DirDados == "" {
		return nil, errors.New("api: Pool, Log, Tentativas e DirDados são obrigatórios")
	}
	if cfg.TamanhoMaxEnvio <= 0 {
		cfg.TamanhoMaxEnvio = TamanhoMaxEnvioPadrao
	}
	if cfg.TimeoutEnvio <= 0 {
		cfg.TimeoutEnvio = TimeoutEnvioPadrao
	}
	if cfg.CotaVideos <= 0 {
		cfg.CotaVideos = CotaVideosPadrao
	}
	servico, err := auth.NovoServico(auth.NovoRepositorio(cfg.Pool), cfg.SegredoJWT)
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}
	a := &aplicacao{
		log:             cfg.Log,
		auth:            servico,
		tentativas:      cfg.Tentativas,
		videos:          videos.NovoServico(cfg.Pool, armazenamento.NovoVolume(cfg.DirDados), cfg.CotaVideos, cfg.Log),
		tamanhoMaxEnvio: cfg.TamanhoMaxEnvio,
		timeoutEnvio:    cfg.TimeoutEnvio,
		metricas:        novasMetricas(cfg.Registro),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /usuarios", a.cadastrar)
	mux.HandleFunc("POST /login", a.entrar)

	// Rotas de Vídeo: só com token.
	rotasVideo := http.NewServeMux()
	rotasVideo.HandleFunc("POST /videos", a.enviar)
	rotasVideo.HandleFunc("GET /videos", a.listar)
	rotasVideo.HandleFunc("GET /videos/{id}", a.consultar)
	rotasVideo.HandleFunc("GET /videos/{id}/zip", a.baixar)
	protegidas := servico.Exigir(rotasVideo)
	mux.Handle(prefixoVideos, protegidas)
	mux.Handle(prefixoVideos+"/", protegidas)
	return cabecalhosDeSeguranca(a.metricas.medir(mux, rotasVideo)), nil
}

// cabecalhosDeSeguranca vale para toda resposta, inclusive erros (OWASP REST
// Security Cheat Sheet). JSON não fica em cache, porque o login devolve o
// token; o download troca o Cache-Control pela política dele.
func cabecalhosDeSeguranca(proximo http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		proximo.ServeHTTP(w, r)
	})
}

type aplicacao struct {
	log             *slog.Logger
	auth            *auth.Servico
	tentativas      *limite.Tentativas
	videos          *videos.Servico
	tamanhoMaxEnvio int64
	timeoutEnvio    time.Duration
	metricas        *metricas
}

// ipDoCliente vem só de RemoteAddr: a API é exposta direto, sem proxy, e o
// X-Forwarded-For seria forjável pelo cliente (ADR-0009).
func ipDoCliente(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// responderSeBloqueado responde 429 com Retry-After se b estourou o limite.
func (a *aplicacao) responderSeBloqueado(w http.ResponseWriter, b limite.Bloqueio) bool {
	if !b.Bloqueado {
		return false
	}
	a.metricas.limitadas.WithLabelValues(causaTentativas).Inc()
	segundos := int(math.Ceil(b.Esperar.Seconds()))
	w.Header().Set("Retry-After", strconv.Itoa(max(segundos, 1)))
	responderErro(w, http.StatusTooManyRequests, "tentativas demais, tente de novo mais tarde")
	return true
}

// tamMaxCorpoJSON limita o corpo de cadastro e login.
const tamMaxCorpoJSON = 4 << 10

type credenciais struct {
	Email string `json:"email"`
	Senha string `json:"senha"`
}

func lerJSON(w http.ResponseWriter, r *http.Request, destino any) bool {
	r.Body = http.MaxBytesReader(respostaOriginal(w), r.Body, tamMaxCorpoJSON)
	if err := json.NewDecoder(r.Body).Decode(destino); err != nil {
		responderErro(w, http.StatusBadRequest, "corpo JSON inválido")
		return false
	}
	return true
}

func responderJSON(w http.ResponseWriter, status int, corpo any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(corpo)
}

// responderErro manda só a mensagem pública; o detalhe vai para o log.
func responderErro(w http.ResponseWriter, status int, mensagem string) {
	responderJSON(w, status, map[string]string{"erro": mensagem})
}

func (a *aplicacao) erroInterno(w http.ResponseWriter, r *http.Request, err error) {
	a.log.ErrorContext(r.Context(), "erro interno", "rota", r.Pattern, "erro", err)
	responderErro(w, http.StatusInternalServerError, "erro interno")
}

func (a *aplicacao) cadastrar(w http.ResponseWriter, r *http.Request) {
	if a.responderSeBloqueado(w, a.tentativas.Cadastro(r.Context(), ipDoCliente(r))) {
		return
	}
	var c credenciais
	if !lerJSON(w, r, &c) {
		return
	}
	err := a.auth.Cadastrar(r.Context(), c.Email, c.Senha)
	var errSenha *auth.ErroSenha
	switch {
	case err == nil:
		w.WriteHeader(http.StatusCreated)
	case errors.As(err, &errSenha):
		responderErro(w, http.StatusBadRequest, errSenha.Error())
	case errors.Is(err, auth.ErrEmailInvalido):
		responderErro(w, http.StatusBadRequest, auth.ErrEmailInvalido.Error())
	case errors.Is(err, auth.ErrEmailEmUso):
		responderErro(w, http.StatusConflict, auth.ErrEmailEmUso.Error())
	default:
		a.erroInterno(w, r, err)
	}
}

type tokenEmitido struct {
	Token    string    `json:"token"`
	ExpiraEm time.Time `json:"expira_em"`
}

func (a *aplicacao) entrar(w http.ResponseWriter, r *http.Request) {
	var c credenciais
	if !lerJSON(w, r, &c) {
		return
	}
	if a.responderSeBloqueado(w, a.tentativas.Login(r.Context(), c.Email, ipDoCliente(r))) {
		return
	}
	token, expira, err := a.auth.Entrar(r.Context(), c.Email, c.Senha)
	switch {
	case err == nil:
		a.tentativas.LoginComSucesso(r.Context(), c.Email)
		responderJSON(w, http.StatusOK, tokenEmitido{Token: token, ExpiraEm: expira.UTC()})
	case errors.Is(err, auth.ErrCredenciaisInvalidas):
		responderErro(w, http.StatusUnauthorized, auth.ErrCredenciaisInvalidas.Error())
	default:
		a.erroInterno(w, r, err)
	}
}
