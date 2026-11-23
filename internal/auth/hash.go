package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parâmetros do argon2id recomendados pelo OWASP (m=19 MiB, t=2, p=1).
const (
	argonMemoriaKiB = 19 * 1024
	argonTempo      = 2
	argonParalelo   = 1
	argonTamSal     = 16
	argonTamChave   = 32

	// hashesSimultaneos limita quantos hashes rodam ao mesmo tempo: cada um
	// reserva 19 MiB, e a API tem 256 MB (ADR-0011).
	hashesSimultaneos = 4
)

var b64 = base64.RawStdEncoding

// hasher calcula e confere hashes argon2id em formato PHC, com um limite de
// cálculos simultâneos para não estourar a memória da API.
type hasher struct {
	vagas chan struct{}
}

func novoHasher() *hasher {
	return &hasher{vagas: make(chan struct{}, hashesSimultaneos)}
}

func (h *hasher) ocupar(ctx context.Context) (func(), error) {
	select {
	case h.vagas <- struct{}{}:
		return func() { <-h.vagas }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("aguardar vaga de hash: %w", ctx.Err())
	}
}

// gerar devolve o hash PHC da senha com sal aleatório.
func (h *hasher) gerar(ctx context.Context, senha string) (string, error) {
	liberar, err := h.ocupar(ctx)
	if err != nil {
		return "", err
	}
	defer liberar()
	sal := make([]byte, argonTamSal)
	if _, err := rand.Read(sal); err != nil {
		return "", fmt.Errorf("gerar sal: %w", err)
	}
	chave := argon2.IDKey([]byte(senha), sal, argonTempo, argonMemoriaKiB, argonParalelo, argonTamChave)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoriaKiB, argonTempo, argonParalelo, b64.EncodeToString(sal), b64.EncodeToString(chave)), nil
}

var errHashInvalido = errors.New("hash PHC inválido")

// confere diz se a senha corresponde ao hash PHC, usando os parâmetros
// gravados no próprio hash.
func (h *hasher) confere(ctx context.Context, senha, phc string) (bool, error) {
	partes := strings.Split(phc, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", sal, chave
	if len(partes) != 6 || partes[1] != "argon2id" {
		return false, errHashInvalido
	}
	var versao int
	if _, err := fmt.Sscanf(partes[2], "v=%d", &versao); err != nil || versao != argon2.Version {
		return false, errHashInvalido
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(partes[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, errHashInvalido
	}
	// Os hashes são todos gerados por este código; recusar parâmetros acima
	// dos nossos impede que um hash adulterado esgote CPU ou memória.
	if m == 0 || m > argonMemoriaKiB || t == 0 || t > argonTempo || p == 0 || p > argonParalelo {
		return false, errHashInvalido
	}
	sal, err := b64.DecodeString(partes[4])
	if err != nil {
		return false, errHashInvalido
	}
	esperada, err := b64.DecodeString(partes[5])
	if err != nil || len(esperada) != argonTamChave {
		return false, errHashInvalido
	}

	liberar, err := h.ocupar(ctx)
	if err != nil {
		return false, err
	}
	defer liberar()
	chave := argon2.IDKey([]byte(senha), sal, t, m, p, argonTamChave)
	return subtle.ConstantTimeCompare(chave, esperada) == 1, nil
}
