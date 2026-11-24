package notificacao

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/mensageria"
	"github.com/LucazDenadai/projeto-fiapx/internal/retentativa"
)

// TimeoutEnvioPadrao é o prazo de cada conversa com o servidor SMTP.
const TimeoutEnvioPadrao = 10 * time.Second

// Enviador é o que o Notificador precisa do servidor de e-mail.
type Enviador interface {
	Enviar(ctx context.Context, de, para string, msg []byte) error
}

// Config do Notificador.
type Config struct {
	Remetente    string               // endereço simples, ex.: nao-responda@fiapx.local
	TimeoutEnvio time.Duration        // por tentativa (padrão TimeoutEnvioPadrao)
	Retry        retentativa.Politica // novas tentativas do SMTP no processo (ADR-0004)
}

// Notificador trata as mensagens da fila notificacao.
type Notificador struct {
	env Enviador
	cfg Config
	log *slog.Logger
}

// NovoNotificador monta o Notificador.
func NovoNotificador(env Enviador, cfg Config, log *slog.Logger) *Notificador {
	if cfg.TimeoutEnvio <= 0 {
		cfg.TimeoutEnvio = TimeoutEnvioPadrao
	}
	cfg.Retry = cfg.Retry.ComPadroes()
	return &Notificador{env: env, cfg: cfg, log: log}
}

// Notificar é o mensageria.Tratador da fila notificacao. Confirma quando o
// e-mail sai e quando a mensagem não tem como virar e-mail (ilegível ou com
// destinatário inválido). Falha do SMTP é transitória: tenta de novo com
// backoff e, esgotado o orçamento, devolve; passado o delivery-limit, a
// mensagem fica parada na notificacao.dlq para inspeção (ADR-0004). Um
// e-mail que não sai não muda o status do Vídeo, que continua falhou.
func (n *Notificador) Notificar(ctx context.Context, corpo []byte) mensageria.Resultado {
	var ev Evento
	if err := json.Unmarshal(corpo, &ev); err != nil {
		n.log.ErrorContext(ctx, "evento falhou ilegível; descartado", "erro", err)
		return mensageria.Confirmar
	}
	// Sem o e-mail no log: é dado pessoal, e o video_id basta para achar.
	log := n.log.With("video_id", ev.VideoID, "correlation_id", ev.CorrelationID)

	msg, err := Montar(ev, n.cfg.Remetente)
	if errors.Is(err, ErrDestinatarioInvalido) {
		log.ErrorContext(ctx, "destinatário inválido no evento; notificação descartada")
		return mensageria.Confirmar
	}
	if err != nil {
		log.ErrorContext(ctx, "montar e-mail; notificação descartada", "erro", err)
		return mensageria.Confirmar
	}
	de, para := strings.Trim(n.cfg.Remetente, "<>"), strings.TrimSpace(ev.Email)
	err = retentativa.TentarSo(ctx, n.cfg.Retry, log, "enviar e-mail", retentativa.Sempre, func() error {
		ctxEnvio, cancelar := context.WithTimeout(ctx, n.cfg.TimeoutEnvio)
		defer cancelar()
		return n.env.Enviar(ctxEnvio, de, para, msg)
	})
	if err != nil {
		log.ErrorContext(ctx, "e-mail não enviado; mensagem devolvida (passado o delivery-limit, fica na notificacao.dlq)", "erro", err)
		return mensageria.Devolver
	}
	log.InfoContext(ctx, "notificação de falha enviada", "motivo", ev.Motivo)
	return mensageria.Confirmar
}
