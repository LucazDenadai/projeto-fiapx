// Package motivo é o Motivo da falha: o código público que explica ao Usuário
// por que um Vídeo teve Falha definitiva, e o texto fixo de cada um. É
// contrato da listagem, do download e da notificação, e nunca leva detalhe
// interno (ADR-0004, ADR-0011).
package motivo

// Códigos do Motivo da falha. O CHECK da coluna videos.motivo aceita só estes.
const (
	VideoInvalido   = "video_invalido"
	VideoMuitoLongo = "video_muito_longo"
	SemFrames       = "sem_frames"
	TempoExcedido   = "tempo_excedido"
	ErroInterno     = "erro_interno"
)

var textos = map[string]string{
	VideoInvalido:   "o arquivo não pôde ser lido como vídeo",
	VideoMuitoLongo: "o vídeo passa da duração máxima",
	SemFrames:       "não foi possível extrair nenhum frame do vídeo",
	TempoExcedido:   "o processamento passou do tempo máximo",
	ErroInterno:     "não conseguimos processar, tente novamente",
}

// Texto devolve a explicação pública do código. Código desconhecido dá o
// texto de ErroInterno: o Usuário nunca vê um código cru nem texto vazio.
func Texto(codigo string) string {
	if t, ok := textos[codigo]; ok {
		return t
	}
	return textos[ErroInterno]
}
