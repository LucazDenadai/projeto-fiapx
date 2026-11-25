package api

import (
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/LucazDenadai/projeto-fiapx/internal/auth"
	"github.com/LucazDenadai/projeto-fiapx/internal/motivo"
	"github.com/LucazDenadai/projeto-fiapx/internal/videos"
)

// Padrões do envio (ADR-0011).
const (
	TamanhoMaxEnvioPadrao = 500 << 20
	TimeoutEnvioPadrao    = 10 * time.Minute
	CotaVideosPadrao      = 5

	limitePadrao = 20
	limiteMax    = 100
)

var errMultipartInvalido = errors.New("multipart inválido")

const errUmArquivo = "envie um único arquivo, no campo video, como multipart/form-data"

// usuarioDe devolve o id do Usuário posto no contexto pelo middleware.
func usuarioDe(r *http.Request) (uuid.UUID, bool) {
	id, ok := auth.UsuarioDe(r.Context())
	if !ok {
		return uuid.UUID{}, false
	}
	u, err := uuid.Parse(id)
	return u, err == nil
}

// nomeDoArquivo lê o filename do Content-Disposition sem o filepath.Base do
// Part.FileName, que depende do sistema operacional; o nome é sanitizado
// depois, no pacote videos.
func nomeDoArquivo(h map[string][]string) (string, bool) {
	cd := ""
	if v := h["Content-Disposition"]; len(v) > 0 {
		cd = v[0]
	}
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return "", false
	}
	nome, ok := params["filename"]
	return nome, ok
}

// enviar recebe o Vídeo em streaming: o arquivo vai direto para o volume, sem
// passar inteiro pela memória (ADR-0006).
func (a *aplicacao) enviar(w http.ResponseWriter, r *http.Request) {
	usuarioID, ok := usuarioDe(r)
	if !ok {
		responderErro(w, http.StatusUnauthorized, "token inválido")
		return
	}
	// Recusa barata: quem está na cota não chega a gravar arquivo.
	if err := a.videos.ConferirCota(r.Context(), usuarioID); err != nil {
		// O corpo não é lido: sem fechar a conexão, um envio grande faria o
		// cliente ver um reset em vez do 429.
		w.Header().Set("Connection", "close")
		a.recusarEnvio(w, r, err)
		return
	}
	// Só esta rota tem prazo de leitura longo; o resto do servidor segue curto.
	if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(a.timeoutEnvio)); err != nil {
		a.log.WarnContext(r.Context(), "definir prazo de leitura do envio", "erro", err)
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.tamanhoMaxEnvio)
	partes, err := r.MultipartReader()
	if err != nil {
		responderErro(w, http.StatusBadRequest, errUmArquivo)
		return
	}

	var recebido *videos.Recebido
	falhar := func(status int, msg string) {
		if recebido != nil {
			a.videos.Descartar(r.Context(), *recebido)
		}
		responderErro(w, status, msg)
	}
	for {
		parte, err := partes.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Multipart malformado é erro do cliente, a não ser o teto.
			var grande *http.MaxBytesError
			if !errors.As(err, &grande) {
				err = errMultipartInvalido
			}
			falhar(a.falhaDeLeitura(r, err))
			return
		}
		nome, ehArquivo := nomeDoArquivo(parte.Header)
		if !ehArquivo {
			continue // campo de texto: ignorado
		}
		if recebido != nil || parte.FormName() != "video" {
			falhar(http.StatusBadRequest, errUmArquivo)
			return
		}
		rec, err := a.videos.Receber(r.Context(), nome, parte)
		if err != nil {
			falhar(a.falhaDeLeitura(r, err))
			return
		}
		recebido = &rec
	}
	if recebido == nil {
		responderErro(w, http.StatusBadRequest, errUmArquivo)
		return
	}

	v, err := a.videos.Aceitar(r.Context(), usuarioID, *recebido)
	if err != nil {
		a.recusarEnvio(w, r, err)
		return
	}
	w.Header().Set("Location", "/videos/"+v.ID.String())
	responderJSON(w, http.StatusAccepted, map[string]string{"id": v.ID.String(), "status": v.Status})
}

// recusarEnvio responde 429 à cota excedida e 500 ao resto.
func (a *aplicacao) recusarEnvio(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, videos.ErrCotaExcedida) {
		responderErro(w, http.StatusTooManyRequests, videos.ErrCotaExcedida.Error())
		return
	}
	a.erroInterno(w, r, err)
}

// falhaDeLeitura traduz o erro de leitura do envio em status e mensagem pública.
func (a *aplicacao) falhaDeLeitura(r *http.Request, err error) (int, string) {
	var grande *http.MaxBytesError
	var rede net.Error // prazo de leitura estourado ou conexão derrubada pelo cliente
	switch {
	case errors.As(err, &rede):
		a.log.WarnContext(r.Context(), "envio interrompido", "erro", err)
		if rede.Timeout() {
			return http.StatusRequestTimeout, "o envio passou do tempo máximo"
		}
		return http.StatusBadRequest, "envio interrompido ou malformado"
	case errors.As(err, &grande):
		return http.StatusRequestEntityTooLarge, "o arquivo passa do tamanho máximo de " + strconv.FormatInt(grande.Limit>>20, 10) + " MB"
	case errors.Is(err, videos.ErrNaoEhVideo):
		return http.StatusUnsupportedMediaType, videos.ErrNaoEhVideo.Error()
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, errMultipartInvalido):
		return http.StatusBadRequest, "envio interrompido ou malformado"
	default:
		a.log.ErrorContext(r.Context(), "erro no envio", "erro", err)
		return http.StatusInternalServerError, "erro interno"
	}
}

// videoJSON é um Vídeo na listagem e na consulta (ADR-0014).
type videoJSON struct {
	ID           string     `json:"id"`
	NomeOriginal string     `json:"nome_original"`
	Status       string     `json:"status"`
	CriadoEm     time.Time  `json:"criado_em"`
	AtualizadoEm time.Time  `json:"atualizado_em"`
	Motivo       string     `json:"motivo,omitempty"`
	MotivoTexto  string     `json:"motivo_texto,omitempty"`
	Download     string     `json:"download,omitempty"`
	ZipExpiraEm  *time.Time `json:"zip_expira_em,omitempty"`
}

func paraJSON(v videos.Video, agora time.Time) videoJSON {
	j := videoJSON{
		ID:           v.ID.String(),
		NomeOriginal: v.NomeOriginal,
		Status:       v.Status,
		CriadoEm:     v.CriadoEm.UTC(),
		AtualizadoEm: v.AtualizadoEm.UTC(),
	}
	if v.Status == videos.StatusFalhou {
		j.Motivo, j.MotivoTexto = v.Motivo, motivo.Texto(v.Motivo)
	}
	if v.ZipExpiraEm != nil {
		expira := v.ZipExpiraEm.UTC()
		j.ZipExpiraEm = &expira
	}
	if v.ZipDisponivel(agora) {
		j.Download = "/videos/" + j.ID + "/zip"
	}
	return j
}

func (a *aplicacao) listar(w http.ResponseWriter, r *http.Request) {
	usuarioID, ok := usuarioDe(r)
	if !ok {
		responderErro(w, http.StatusUnauthorized, "token inválido")
		return
	}
	limite := limitePadrao
	if v := r.URL.Query().Get("limite"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > limiteMax {
			responderErro(w, http.StatusBadRequest, "limite deve ser um número de 1 a "+strconv.Itoa(limiteMax))
			return
		}
		limite = n
	}
	pag, err := a.videos.Listar(r.Context(), usuarioID, r.URL.Query().Get("cursor"), limite)
	if errors.Is(err, videos.ErrCursorInvalido) {
		responderErro(w, http.StatusBadRequest, videos.ErrCursorInvalido.Error())
		return
	}
	if err != nil {
		a.erroInterno(w, r, err)
		return
	}
	agora := time.Now()
	resp := struct {
		Videos  []videoJSON `json:"videos"`
		Proximo string      `json:"proximo_cursor,omitempty"`
	}{Videos: make([]videoJSON, 0, len(pag.Videos)), Proximo: pag.Proximo}
	for _, v := range pag.Videos {
		resp.Videos = append(resp.Videos, paraJSON(v, agora))
	}
	responderJSON(w, http.StatusOK, resp)
}

// videoDaRota lê o Usuário do token e o id do Vídeo da rota. Responde e
// devolve false se faltar um deles: id malformado dá o mesmo 404 do Vídeo
// inexistente.
func videoDaRota(w http.ResponseWriter, r *http.Request) (usuarioID, id uuid.UUID, ok bool) {
	usuarioID, ok = usuarioDe(r)
	if !ok {
		responderErro(w, http.StatusUnauthorized, "token inválido")
		return usuarioID, id, false
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		responderErro(w, http.StatusNotFound, videos.ErrNaoEncontrado.Error())
		return usuarioID, id, false
	}
	return usuarioID, id, true
}

func (a *aplicacao) consultar(w http.ResponseWriter, r *http.Request) {
	usuarioID, id, ok := videoDaRota(w, r)
	if !ok {
		return
	}
	v, err := a.videos.Buscar(r.Context(), usuarioID, id)
	if errors.Is(err, videos.ErrNaoEncontrado) {
		responderErro(w, http.StatusNotFound, videos.ErrNaoEncontrado.Error())
		return
	}
	if err != nil {
		a.erroInterno(w, r, err)
		return
	}
	responderJSON(w, http.StatusOK, paraJSON(v, time.Now()))
}

// baixar serve o Zip de frames com ServeContent: Range (download retomável),
// If-Modified-Since e HEAD saem de graça (ADR-0006).
func (a *aplicacao) baixar(w http.ResponseWriter, r *http.Request) {
	usuarioID, id, ok := videoDaRota(w, r)
	if !ok {
		return
	}
	zip, err := a.videos.AbrirZip(r.Context(), usuarioID, id, time.Now())
	var naoConcluido *videos.ErroNaoConcluido
	switch {
	case errors.As(err, &naoConcluido):
		responderNaoConcluido(w, naoConcluido)
		return
	case errors.Is(err, videos.ErrNaoEncontrado):
		responderErro(w, http.StatusNotFound, videos.ErrNaoEncontrado.Error())
		return
	case errors.Is(err, videos.ErrZipExpirado):
		responderErro(w, http.StatusGone, videos.ErrZipExpirado.Error())
		return
	case err != nil:
		a.erroInterno(w, r, err)
		return
	}
	defer func() { _ = zip.Arquivo.Close() }()
	info, err := zip.Arquivo.Stat()
	if err != nil {
		a.erroInterno(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	// FormatMediaType cita ou codifica (RFC 2231) o nome, que vem do Usuário:
	// aspas, ponto e vírgula e acentos não quebram nem injetam o header.
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": zip.Nome}))
	h.Set("Cache-Control", "private, no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, zip.Nome, info.ModTime(), zip.Arquivo)
}

// responderNaoConcluido responde 409 com o status e, se o Vídeo falhou, o
// Motivo da falha e o texto fixo dele (ADR-0006).
func responderNaoConcluido(w http.ResponseWriter, e *videos.ErroNaoConcluido) {
	corpo := map[string]string{"erro": e.Error(), "status": e.Video.Status}
	if e.Video.Status == videos.StatusFalhou {
		corpo["motivo"], corpo["motivo_texto"] = e.Video.Motivo, motivo.Texto(e.Video.Motivo)
	}
	responderJSON(w, http.StatusConflict, corpo)
}
