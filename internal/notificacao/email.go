// Package notificacao é o Notifier: consome o evento falhou e avisa o Usuário
// da Falha definitiva por e-mail, em texto puro (ADR-0008). Não acessa o
// Postgres: tudo o que o e-mail precisa vem no evento.
package notificacao

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	_ "time/tzdata" // o fuso de Brasília, mesmo em imagem sem /usr/share/zoneinfo
	"unicode"

	"github.com/google/uuid"

	"github.com/LucazDenadai/projeto-fiapx/internal/motivo"
)

// Evento é o falhou que o Worker grava na outbox (ADR-0008).
type Evento struct {
	VideoID       string    `json:"video_id"`
	CorrelationID string    `json:"correlation_id"`
	Email         string    `json:"email"`
	NomeOriginal  string    `json:"nome_original"`
	Motivo        string    `json:"motivo"`
	OcorridoEm    time.Time `json:"ocorrido_em"`
}

// ErrDestinatarioInvalido: o e-mail do evento não é um endereço simples.
// Tentar de novo não conserta.
var ErrDestinatarioInvalido = errors.New("destinatário inválido")

// Assunto do e-mail de Falha definitiva (ADR-0008).
const Assunto = "Não conseguimos processar seu vídeo"

var brasilia = func() *time.Location {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		panic(err) // embutido por time/tzdata: só falha se o pacote sumir
	}
	return loc
}()

// Email é o e-mail pronto para o SMTP: o envelope (De, Para), já validado,
// e a Mensagem para o DATA.
type Email struct {
	De, Para string
	Mensagem []byte
}

// Montar escreve o e-mail de Falha definitiva do evento: texto puro em UTF-8,
// sem HTML nem links. Dado do Usuário só entra no corpo; os cabeçalhos vêm de
// endereços validados e de texto fixo, o que impede injeção de cabeçalho.
func Montar(ev Evento, remetente string) (Email, error) {
	para, err := endereco(ev.Email)
	if err != nil {
		return Email{}, err
	}
	de, err := endereco(remetente)
	if err != nil {
		return Email{}, fmt.Errorf("remetente: %w", err)
	}

	var b bytes.Buffer
	cabecalho := func(nome, valor string) {
		b.WriteString(nome)
		b.WriteString(": ")
		b.WriteString(valor)
		b.WriteString("\r\n")
	}
	cabecalho("From", "<"+de+">")
	cabecalho("To", "<"+para+">")
	cabecalho("Subject", mime.QEncoding.Encode("utf-8", Assunto))
	cabecalho("Date", time.Now().Format(time.RFC1123Z))
	_, dominio, _ := strings.Cut(de, "@")
	cabecalho("Message-ID", "<"+uuid.NewString()+"@"+dominio+">")
	cabecalho("MIME-Version", "1.0")
	cabecalho("Content-Type", "text/plain; charset=UTF-8")
	cabecalho("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")

	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(corpo(ev))); err != nil {
		return Email{}, fmt.Errorf("corpo: %w", err)
	}
	if err := qp.Close(); err != nil {
		return Email{}, fmt.Errorf("corpo: %w", err)
	}
	return Email{De: de, Para: para, Mensagem: b.Bytes()}, nil
}

func corpo(ev Evento) string {
	abertura := fmt.Sprintf("Não conseguimos processar o seu vídeo \"%s\".", umaLinha(ev.NomeOriginal))
	// Sem a data no evento, a frase sai sem ela: nunca "01/01/0001".
	if !ev.OcorridoEm.IsZero() {
		abertura += " A falha foi registrada em " + ev.OcorridoEm.In(brasilia).Format("02/01/2006 às 15:04") +
			" (horário de Brasília)."
	}
	linhas := []string{
		"Olá,",
		"",
		abertura,
		"",
		"Motivo: " + motivo.Texto(ev.Motivo) + ".",
		"",
		"Se quiser tentar de novo, envie o vídeo de novo: ele entra como um envio novo.",
		"",
		"Identificador do vídeo: " + umaLinha(ev.VideoID),
		"",
		"FIAP X",
	}
	return strings.Join(linhas, "\r\n") + "\r\n"
}

// endereco valida um endereço simples (sem nome, sem lista) e o devolve.
// Quebra de linha ou qualquer outra coisa dá ErrDestinatarioInvalido.
func endereco(s string) (string, error) {
	if strings.ContainsAny(s, "\r\n") {
		return "", ErrDestinatarioInvalido
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s {
		return "", ErrDestinatarioInvalido
	}
	return a.Address, nil
}

// umaLinha troca controles e caracteres de formatação (quebras, RLO) por
// espaço: o nome do arquivo vem do Usuário.
func umaLinha(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
