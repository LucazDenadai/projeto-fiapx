// Package auth cadastra Usuários, confere credenciais e emite e valida o JWT
// que identifica o Usuário nas rotas de Vídeo (ADR-0007).
package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// Erros com mensagem pública, que a API pode devolver ao cliente.
var (
	ErrEmailInvalido        = errors.New("e-mail inválido")
	ErrEmailEmUso           = errors.New("e-mail já cadastrado")
	ErrCredenciaisInvalidas = errors.New("credenciais inválidas")
)

// Servico é o ponto de entrada do cadastro, do login e da validação do token.
type Servico struct {
	usuarios Usuarios
	hasher   *hasher
	emissor  *emissor
	// hashFicticio é comparado no login de e-mail inexistente, para que o
	// tempo de resposta não revele se o e-mail tem conta.
	hashFicticio string
}

// NovoServico monta o serviço. Falha se o segredo do JWT for curto demais.
func NovoServico(usuarios Usuarios, segredoJWT []byte) (*Servico, error) {
	e, err := novoEmissor(segredoJWT)
	if err != nil {
		return nil, err
	}
	h := novoHasher()
	aleatoria := rand.Text()
	ficticio, err := h.gerar(context.Background(), aleatoria)
	if err != nil {
		return nil, fmt.Errorf("gerar hash fictício: %w", err)
	}
	return &Servico{usuarios: usuarios, hasher: h, emissor: e, hashFicticio: ficticio}, nil
}

// Cadastrar cria um Usuário. Erros esperados: ErrEmailInvalido, *ErroSenha e
// ErrEmailEmUso.
func (s *Servico) Cadastrar(ctx context.Context, email, senha string) error {
	email, err := normalizarEmail(email)
	if err != nil {
		return err
	}
	if err := validarSenha(senha); err != nil {
		return err
	}
	hash, err := s.hasher.gerar(ctx, senha)
	if err != nil {
		return fmt.Errorf("cadastrar: %w", err)
	}
	if err := s.usuarios.Criar(ctx, email, hash); err != nil {
		return fmt.Errorf("cadastrar: %w", err)
	}
	return nil
}

// Entrar confere as credenciais e emite o token. E-mail inexistente e senha
// errada dão o mesmo ErrCredenciaisInvalidas.
func (s *Servico) Entrar(ctx context.Context, email, senha string) (token string, expira time.Time, err error) {
	id, hash := "", s.hashFicticio
	if normalizado, errEmail := normalizarEmail(email); errEmail == nil {
		idEncontrado, hashEncontrado, errBusca := s.usuarios.BuscarPorEmail(ctx, normalizado)
		switch {
		case errBusca == nil:
			id, hash = idEncontrado, hashEncontrado
		case !errors.Is(errBusca, errNaoEncontrado):
			return "", time.Time{}, fmt.Errorf("entrar: %w", errBusca)
		}
	}
	ok, err := s.hasher.confere(ctx, senha, hash)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("conferir senha: %w", err)
	}
	if !ok || id == "" {
		return "", time.Time{}, ErrCredenciaisInvalidas
	}
	return s.emissor.emitir(id, time.Now())
}

// tamMaxEmail é o limite prático de um endereço (RFC 5321).
const tamMaxEmail = 254

// normalizarEmail aceita só um endereço puro (sem nome de exibição) e o
// devolve em minúsculas.
func normalizarEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || len(email) > tamMaxEmail {
		return "", ErrEmailInvalido
	}
	end, err := mail.ParseAddress(email)
	if err != nil || end.Address != email {
		return "", ErrEmailInvalido
	}
	return email, nil
}
