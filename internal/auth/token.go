package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Validade do token (ADR-0007). Sem refresh: expirou, faz login de novo.
const validadeToken = time.Hour

// tamMinSegredo é o tamanho mínimo do segredo do JWT: 256 bits, o tamanho da
// saída do HMAC-SHA256.
const tamMinSegredo = 32

// errTokenInvalido cobre token ausente, malformado, expirado, com outro
// algoritmo ou com assinatura errada.
var errTokenInvalido = errors.New("token inválido")

// emissor assina e valida JWT HS256 com claims sub, iat e exp.
type emissor struct {
	segredo []byte
}

func novoEmissor(segredo []byte) (*emissor, error) {
	if len(segredo) < tamMinSegredo {
		return nil, fmt.Errorf("segredo do JWT precisa ter ao menos %d bytes", tamMinSegredo)
	}
	return &emissor{segredo: segredo}, nil
}

func (e *emissor) emitir(usuarioID string, agora time.Time) (string, time.Time, error) {
	agora = agora.Truncate(time.Second) // JWT guarda segundos inteiros
	expira := agora.Add(validadeToken)
	claims := jwt.RegisteredClaims{
		Subject:   usuarioID,
		IssuedAt:  jwt.NewNumericDate(agora),
		ExpiresAt: jwt.NewNumericDate(expira),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(e.segredo)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("assinar token: %w", err)
	}
	return token, expira, nil
}

// validar devolve o id do Usuário do token. Exige HS256 explicitamente, o que
// rejeita alg none e troca de algoritmo, e exige exp no futuro.
func (e *emissor) validar(token string) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return e.segredo, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil || claims.Subject == "" {
		return "", errTokenInvalido
	}
	return claims.Subject, nil
}
