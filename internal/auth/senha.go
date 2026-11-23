package auth

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Regra de composição da senha (ADR-0007).
const (
	senhaMin = 8
	senhaMax = 128
)

// ErroSenha diz tudo o que falta para a senha cumprir a regra. A mensagem é
// pública.
type ErroSenha struct {
	Faltas []string
}

func (e *ErroSenha) Error() string {
	return "a senha precisa ter " + strings.Join(e.Faltas, ", ")
}

// validarSenha devolve *ErroSenha se a senha não cumpre a regra, ou nil.
func validarSenha(senha string) error {
	var faltas []string
	if n := utf8.RuneCountInString(senha); n < senhaMin || n > senhaMax {
		faltas = append(faltas, "de 8 a 128 caracteres")
	}
	var maius, minus, numero, especial bool
	for _, r := range senha {
		switch {
		case unicode.IsUpper(r):
			maius = true
		case unicode.IsLower(r):
			minus = true
		case unicode.IsDigit(r):
			numero = true
		case !unicode.IsLetter(r) && !unicode.IsSpace(r) && unicode.IsPrint(r):
			especial = true
		}
	}
	for _, c := range []struct {
		ok    bool
		falta string
	}{
		{maius, "uma letra maiúscula"},
		{minus, "uma letra minúscula"},
		{numero, "um número"},
		{especial, "um caractere especial"},
	} {
		if !c.ok {
			faltas = append(faltas, c.falta)
		}
	}
	if len(faltas) > 0 {
		return &ErroSenha{Faltas: faltas}
	}
	return nil
}
