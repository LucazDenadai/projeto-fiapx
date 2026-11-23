package videos

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tamCabecalho é quanto do início do arquivo basta para reconhecer o formato.
const tamCabecalho = 512

// pareceVideo confere os magic bytes do início do arquivo (ADR-0006). É uma
// triagem barata: a validação profunda é do Worker, com o ffprobe.
func pareceVideo(c []byte) bool {
	switch {
	case len(c) >= 12 && string(c[4:8]) == "ftyp": // MP4, MOV, 3GP, M4V
		return true
	case bytes.HasPrefix(c, []byte{0x1A, 0x45, 0xDF, 0xA3}): // Matroska, WebM
		return true
	case len(c) >= 12 && string(c[0:4]) == "RIFF" && string(c[8:12]) == "AVI ":
		return true
	case bytes.HasPrefix(c, []byte{0x00, 0x00, 0x01, 0xBA}), bytes.HasPrefix(c, []byte{0x00, 0x00, 0x01, 0xB3}): // MPEG-PS, MPEG-1/2
		return true
	case len(c) > 188 && c[0] == 0x47 && c[188] == 0x47: // MPEG-TS
		return true
	case bytes.HasPrefix(c, []byte("FLV")):
		return true
	case bytes.HasPrefix(c, []byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11}): // ASF, WMV
		return true
	}
	return false
}

// tamMaxNome limita o nome original, guardado só para exibição.
const tamMaxNome = 255

// sanitizarNome tira caracteres de controle e troca separadores de caminho e
// ':' por '_', com no máximo tamMaxNome caracteres. Vazio vira "video".
func sanitizarNome(nome string) string {
	var b strings.Builder
	n := 0
	for _, r := range nome {
		if n == tamMaxNome {
			break
		}
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			continue
		case r == '/', r == '\\', r == ':':
			r = '_'
		}
		b.WriteRune(r)
		n++
	}
	limpo := strings.TrimSpace(b.String())
	if limpo == "" || limpo == "." || limpo == ".." {
		return "video"
	}
	return limpo
}
