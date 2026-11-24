package notificacao_test

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"net/mail"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LucazDenadai/projeto-fiapx/internal/notificacao"
)

func eventoDeAna() notificacao.Evento {
	return notificacao.Evento{
		VideoID:       "0b6f8f3e-1d2c-4c5b-9a8e-7f6d5c4b3a21",
		CorrelationID: "7d1c0f9a-corr",
		Email:         "ana@ex.com",
		NomeOriginal:  "férias na praia.mp4",
		Motivo:        "video_muito_longo",
		// 12:30 UTC = 09:30 em Brasília.
		OcorridoEm: time.Date(2026, 11, 24, 12, 30, 0, 0, time.UTC),
	}
}

func ler(t *testing.T, bruto []byte) (*mail.Message, string) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(bruto))
	if err != nil {
		t.Fatalf("e-mail ilegível: %v\n%s", err, bruto)
	}
	corpo, err := io.ReadAll(msg.Body)
	if err != nil {
		t.Fatal(err)
	}
	return msg, decodificarQP(t, corpo)
}

func TestMontar_EmailDeFalha(t *testing.T) {
	email, err := notificacao.Montar(eventoDeAna(), "nao-responda@fiapx.local")
	if err != nil {
		t.Fatalf("montar: %v", err)
	}
	if email.De != "nao-responda@fiapx.local" || email.Para != "ana@ex.com" {
		t.Errorf("envelope = %q → %q", email.De, email.Para)
	}
	msg, corpo := ler(t, email.Mensagem)

	assunto, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || assunto != "Não conseguimos processar seu vídeo" {
		t.Errorf("assunto = %q (%v)", assunto, err)
	}
	if got := msg.Header.Get("To"); got != "<ana@ex.com>" {
		t.Errorf("To = %q", got)
	}
	if got := msg.Header.Get("From"); got != "<nao-responda@fiapx.local>" {
		t.Errorf("From = %q", got)
	}
	if got := msg.Header.Get("Content-Type"); got != "text/plain; charset=UTF-8" {
		t.Errorf("Content-Type = %q", got)
	}
	for _, quero := range []string{"férias na praia.mp4", "24/11/2026 às 09:30", "o vídeo passa da duração máxima", "envie o vídeo de novo"} {
		if !strings.Contains(corpo, quero) {
			t.Errorf("corpo sem %q:\n%s", quero, corpo)
		}
	}
	// Sem HTML, links nem ids técnicos além do video_id.
	for _, proibido := range []string{"7d1c0f9a-corr", "http", "<", "video_muito_longo"} {
		if strings.Contains(corpo, proibido) {
			t.Errorf("corpo contém %q:\n%s", proibido, corpo)
		}
	}
}

// O nome original vem do Usuário. Mesmo com quebra de linha, não cria
// cabeçalho: ele só vai para o corpo, numa linha só.
func TestMontar_NomeComQuebraDeLinhaNaoCriaCabecalho(t *testing.T) {
	ev := eventoDeAna()
	ev.NomeOriginal = "video.mp4\r\nBcc: espiao@ex.com\r\n\r\nX-Outro: 1"

	email, err := notificacao.Montar(ev, "nao-responda@fiapx.local")
	if err != nil {
		t.Fatalf("montar: %v", err)
	}
	msg, corpo := ler(t, email.Mensagem)

	var nomes []string
	for k := range msg.Header {
		nomes = append(nomes, k)
	}
	slices.Sort(nomes)
	quero := []string{"Content-Transfer-Encoding", "Content-Type", "Date", "From", "Message-Id", "Mime-Version", "Subject", "To"}
	if !slices.Equal(nomes, quero) {
		t.Errorf("cabeçalhos = %v, quero %v", nomes, quero)
	}
	for linha := range strings.Lines(corpo) {
		if strings.HasPrefix(linha, "Bcc:") || strings.HasPrefix(linha, "X-Outro:") {
			t.Errorf("linha injetada no corpo: %q", linha)
		}
	}
	if !strings.Contains(corpo, "video.mp4 Bcc: espiao@ex.com X-Outro: 1") {
		t.Errorf("nome não ficou numa linha só:\n%s", corpo)
	}
}

// Destinatário com quebra de linha ou que não é endereço não tem conserto:
// erro próprio, para quem chama descartar a mensagem.
func TestMontar_DestinatarioInvalido(t *testing.T) {
	for _, email := range []string{"ana@ex.com\r\nBcc: espiao@ex.com", "nao é email", "", "Ana <ana@ex.com>, beto@ex.com"} {
		ev := eventoDeAna()
		ev.Email = email
		if _, err := notificacao.Montar(ev, "nao-responda@fiapx.local"); !errors.Is(err, notificacao.ErrDestinatarioInvalido) {
			t.Errorf("%q: erro = %v, quero ErrDestinatarioInvalido", email, err)
		}
	}
}

func TestMontar_MotivoDesconhecidoUsaTextoGenerico(t *testing.T) {
	ev := eventoDeAna()
	ev.Motivo = "codigo_novo"
	email, err := notificacao.Montar(ev, "nao-responda@fiapx.local")
	if err != nil {
		t.Fatal(err)
	}
	if _, corpo := ler(t, email.Mensagem); !strings.Contains(corpo, "não conseguimos processar, tente novamente") || strings.Contains(corpo, "codigo_novo") {
		t.Errorf("corpo:\n%s", corpo)
	}
}

// Evento sem ocorrido_em: o e-mail sai sem a data, nunca com 01/01/0001.
func TestMontar_SemDataOmiteAFrase(t *testing.T) {
	ev := eventoDeAna()
	ev.OcorridoEm = time.Time{}
	email, err := notificacao.Montar(ev, "nao-responda@fiapx.local")
	if err != nil {
		t.Fatal(err)
	}
	if _, corpo := ler(t, email.Mensagem); strings.Contains(corpo, "0001") || strings.Contains(corpo, "registrada em") {
		t.Errorf("corpo com data zero:\n%s", corpo)
	}
}
