// Package retentativa é a primeira camada do retry do ADR-0004: dentro do
// processo, a operação de infraestrutura que falhou tenta de novo com backoff
// exponencial até um orçamento. Esgotado, quem chama devolve a mensagem para a
// fila, e o delivery-limit faz a segunda camada.
package retentativa

import (
	"context"
	"log/slog"
	"time"
)

// Padrões do ADR-0004: 1, 2, 4, 8, 16 s… até ~1 min.
const (
	InicialPadrao   = time.Second
	OrcamentoPadrao = time.Minute
)

// Politica diz quanto esperar antes da primeira nova tentativa e por quanto
// tempo, no total, continuar tentando. Zero num campo usa o padrão.
type Politica struct {
	Inicial   time.Duration
	Orcamento time.Duration
}

// ComPadroes preenche os campos zerados.
func (p Politica) ComPadroes() Politica {
	if p.Inicial <= 0 {
		p.Inicial = InicialPadrao
	}
	if p.Orcamento <= 0 {
		p.Orcamento = OrcamentoPadrao
	}
	return p
}

// Tentar roda op e, enquanto o erro for transitório e houver orçamento, espera
// e roda de novo, dobrando a espera; a última espera encolhe para caber no
// orçamento. Para na hora se ctx acabar. Devolve o último resultado.
func Tentar[T any](ctx context.Context, pol Politica, log *slog.Logger, operacao string,
	transitorio func(error) bool, op func() (T, error)) (T, error) {
	pol = pol.ComPadroes()
	limite := time.Now().Add(pol.Orcamento)
	espera := pol.Inicial
	for {
		v, err := op()
		resta := time.Until(limite)
		if err == nil || ctx.Err() != nil || !transitorio(err) || resta <= 0 {
			return v, err
		}
		espera = min(espera, resta)
		log.WarnContext(ctx, "infraestrutura falhou; tentando de novo", "operacao", operacao, "espera", espera.String(), "erro", err)
		select {
		case <-ctx.Done():
			return v, err
		case <-time.After(espera):
		}
		espera *= 2
	}
}

// TentarSo é Tentar para operações sem resultado.
func TentarSo(ctx context.Context, pol Politica, log *slog.Logger, operacao string,
	transitorio func(error) bool, op func() error) error {
	_, err := Tentar(ctx, pol, log, operacao, transitorio, func() (struct{}, error) { return struct{}{}, op() })
	return err
}

// Sempre trata todo erro como transitório.
func Sempre(error) bool { return true }
