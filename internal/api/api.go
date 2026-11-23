// Package api monta o roteador HTTP público da API: cadastro, login e as
// rotas de Vídeo protegidas pelo JWT.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LucazDenadai/projeto-fiapx/internal/auth"
)

// Config reúne as dependências da API.
type Config struct {
	Pool       *pgxpool.Pool
	SegredoJWT []byte
	Log        *slog.Logger
}

// Novo devolve o roteador público. Falha se a configuração estiver incompleta,
// por exemplo sem o segredo do JWT.
func Novo(cfg Config) (http.Handler, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("api: Pool e Log são obrigatórios")
	}
	servico, err := auth.NovoServico(auth.NovoRepositorio(cfg.Pool), cfg.SegredoJWT)
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}
	a := &aplicacao{log: cfg.Log, auth: servico}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /usuarios", a.cadastrar)
	mux.HandleFunc("POST /login", a.entrar)

	// Rotas de Vídeo: só com token. Os handlers chegam no ticket #23.
	videos := servico.Exigir(http.HandlerFunc(naoImplementado))
	mux.Handle("/videos", videos)
	mux.Handle("/videos/", videos)
	return mux, nil
}

type aplicacao struct {
	log  *slog.Logger
	auth *auth.Servico
}

// tamMaxCorpoJSON limita o corpo de cadastro e login.
const tamMaxCorpoJSON = 4 << 10

type credenciais struct {
	Email string `json:"email"`
	Senha string `json:"senha"`
}

func lerJSON(w http.ResponseWriter, r *http.Request, destino any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, tamMaxCorpoJSON)
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
	token, expira, err := a.auth.Entrar(r.Context(), c.Email, c.Senha)
	switch {
	case err == nil:
		responderJSON(w, http.StatusOK, tokenEmitido{Token: token, ExpiraEm: expira.UTC()})
	case errors.Is(err, auth.ErrCredenciaisInvalidas):
		responderErro(w, http.StatusUnauthorized, auth.ErrCredenciaisInvalidas.Error())
	default:
		a.erroInterno(w, r, err)
	}
}

func naoImplementado(w http.ResponseWriter, _ *http.Request) {
	responderErro(w, http.StatusNotImplemented, "ainda não implementado")
}
