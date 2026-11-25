// Package pico simula um pico de envios contra a API, pela interface pública
// dela, para demonstrar a escala dos Workers (ADR-0010, ADR-0011).
package pico

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// porUsuario é a cota padrão (ADR-0011): mais que isso a API recusa.
	porUsuario      = 5
	intervaloPadrao = time.Second
	// timeoutRequisicao acompanha o timeout de envio da API.
	timeoutRequisicao = 10 * time.Minute
	// senha dos Usuários de demonstração, que só têm vídeos de teste.
	senha = "Pico#Demo-123"
)

// Config do pico.
type Config struct {
	URL    string // da API
	Video  string // arquivo enviado em todos os envios
	Envios int
	// Esperar acompanha os Vídeos aceitos até todos terminarem.
	Esperar   bool
	Intervalo time.Duration // entre consultas ao esperar (padrão 1 s)
}

// Resultado do pico.
type Resultado struct {
	Aceitos, Recusados int // por cota (429)
	Concluidos, Falhos int // só com Esperar
	DuracaoEnvios      time.Duration
	DuracaoTotal       time.Duration // dos envios até o último Vídeo terminar
}

// Rodar cadastra (ou reusa) os Usuários pico-N, dispara todos os envios em
// paralelo e, se pedido, espera os Vídeos terminarem, com o progresso em saida.
// Envio recusado por cota conta em Recusados; qualquer outro erro é devolvido.
func Rodar(ctx context.Context, cfg Config, saida io.Writer) (Resultado, error) {
	if cfg.Intervalo <= 0 {
		cfg.Intervalo = intervaloPadrao
	}
	c := cliente{url: cfg.URL, http: &http.Client{Timeout: timeoutRequisicao}}
	// Os tokens vêm antes, em sequência: login e cadastro têm limite de
	// tentativas por IP (ADR-0009), e o pico é dos envios.
	var tokens []string
	for i := range (cfg.Envios + porUsuario - 1) / porUsuario {
		token, err := c.token(ctx, fmt.Sprintf("pico-%d@fiapx.local", i+1))
		if err != nil {
			return Resultado{}, err
		}
		tokens = append(tokens, token)
	}
	_, _ = fmt.Fprintf(saida, "%d envios em paralelo, de %d Usuários\n", cfg.Envios, len(tokens))

	inicio := time.Now()
	var (
		r       Resultado
		mu      sync.Mutex
		wg      sync.WaitGroup
		erros   []error
		aceitos = map[string]map[string]bool{} // token → ids
	)
	for i := range cfg.Envios {
		token := tokens[i/porUsuario]
		wg.Go(func() {
			id, err := c.enviar(ctx, token, cfg.Video)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case errors.Is(err, errCota):
				r.Recusados++
			case err != nil:
				erros = append(erros, err)
			default:
				r.Aceitos++
				if aceitos[token] == nil {
					aceitos[token] = map[string]bool{}
				}
				aceitos[token][id] = true
			}
		})
	}
	wg.Wait()
	r.DuracaoEnvios = time.Since(inicio)
	_, _ = fmt.Fprintf(saida, "aceitos %d, recusados por cota %d, erros %d em %v\n",
		r.Aceitos, r.Recusados, len(erros), r.DuracaoEnvios.Round(time.Millisecond))
	if cfg.Esperar {
		if err := c.acompanhar(ctx, aceitos, cfg.Intervalo, inicio, &r, saida); err != nil {
			erros = append(erros, err)
		}
	}
	r.DuracaoTotal = time.Since(inicio)
	return r, errors.Join(erros...)
}

// acompanhar mostra o progresso até todos os Vídeos aceitos terminarem.
func (c cliente) acompanhar(ctx context.Context, aceitos map[string]map[string]bool, intervalo time.Duration, inicio time.Time, r *Resultado, saida io.Writer) error {
	ultimo := ""
	for {
		n := map[string]int{}
		for token, ids := range aceitos {
			if err := c.contar(ctx, token, ids, n); err != nil {
				return err
			}
		}
		linha := fmt.Sprintf("recebido %d  processando %d  concluido %d  falhou %d",
			n["recebido"], n["processando"], n["concluido"], n["falhou"])
		if linha != ultimo {
			_, _ = fmt.Fprintf(saida, "%6.1fs  %s\n", time.Since(inicio).Seconds(), linha)
			ultimo = linha
		}
		r.Concluidos, r.Falhos = n["concluido"], n["falhou"]
		if r.Concluidos+r.Falhos >= r.Aceitos {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("acompanhar os vídeos: %w", ctx.Err())
		case <-time.After(intervalo):
		}
	}
}

type cliente struct {
	url  string
	http *http.Client
}

// token faz login; na primeira vez, cadastra o Usuário antes.
func (c cliente) token(ctx context.Context, email string) (string, error) {
	cred, err := json.Marshal(map[string]string{"email": email, "senha": senha})
	if err != nil {
		return "", fmt.Errorf("montar credenciais: %w", err)
	}
	entrar := func() (int, string, error) {
		var corpo struct {
			Token string `json:"token"`
		}
		status, err := c.fazer(ctx, http.MethodPost, "/login", "", "application/json", bytes.NewReader(cred), &corpo)
		return status, corpo.Token, err
	}
	status, token, err := entrar()
	if err == nil && status == http.StatusUnauthorized {
		status, err = c.fazer(ctx, http.MethodPost, "/usuarios", "", "application/json", bytes.NewReader(cred), nil)
		if err == nil && status == http.StatusTooManyRequests {
			return "", fmt.Errorf("cadastrar %s: limite de cadastros por IP atingido; use menos envios ou aumente LIMITE_CADASTRO_IP_MAX", email)
		}
		if err == nil && status != http.StatusCreated {
			return "", fmt.Errorf("cadastrar %s: status %d", email, status)
		}
		if err == nil {
			status, token, err = entrar()
		}
	}
	if err != nil {
		return "", fmt.Errorf("obter token de %s: %w", email, err)
	}
	if status == http.StatusTooManyRequests {
		return "", fmt.Errorf("login de %s: limite de tentativas atingido; espere a janela passar", email)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("login de %s: status %d", email, status)
	}
	return token, nil
}

var errCota = errors.New("cota do usuário atingida")

// enviar manda o arquivo em streaming, como a API o recebe, e devolve o id.
func (c cliente) enviar(ctx context.Context, token, caminho string) (string, error) {
	f, err := os.Open(caminho) //nolint:gosec // arquivo escolhido por quem roda o pico
	if err != nil {
		return "", fmt.Errorf("abrir vídeo: %w", err)
	}
	defer func() { _ = f.Close() }()
	leitor, escritor := io.Pipe()
	defer func() { _ = leitor.Close() }() // solta a goroutine se o envio acabar antes
	mp := multipart.NewWriter(escritor)
	go func() {
		parte, err := mp.CreateFormFile("video", filepath.Base(caminho))
		if err == nil {
			_, err = io.Copy(parte, f)
		}
		if err == nil {
			err = mp.Close()
		}
		_ = escritor.CloseWithError(err)
	}()
	var corpo struct {
		ID string `json:"id"`
	}
	status, err := c.fazer(ctx, http.MethodPost, "/videos", token, mp.FormDataContentType(), leitor, &corpo)
	switch {
	case err != nil:
		return "", fmt.Errorf("enviar: %w", err)
	case status == http.StatusTooManyRequests:
		return "", errCota
	case status != http.StatusAccepted:
		return "", fmt.Errorf("enviar: status %d", status)
	}
	return corpo.ID, nil
}

// contar soma em n os status dos Vídeos de ids.
func (c cliente) contar(ctx context.Context, token string, ids map[string]bool, n map[string]int) error {
	// Os Vídeos do pico são os mais recentes do Usuário: cabem na 1ª página.
	var corpo struct {
		Videos []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"videos"`
	}
	status, err := c.fazer(ctx, http.MethodGet, "/videos?limite=100", token, "", nil, &corpo)
	if err != nil {
		return fmt.Errorf("listar: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("listar: status %d", status)
	}
	for _, v := range corpo.Videos {
		if ids[v.ID] {
			n[v.Status]++
		}
	}
	return nil
}

// fazer manda a requisição e decodifica a resposta 2xx em destino, se houver.
func (c cliente) fazer(ctx context.Context, metodo, rota, token, tipo string, corpo io.Reader, destino any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, metodo, c.url+rota, corpo)
	if err != nil {
		return 0, err
	}
	if tipo != "" {
		req.Header.Set("Content-Type", tipo)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = res.Body.Close() }()
	if destino != nil && res.StatusCode < http.StatusMultipleChoices {
		if err := json.NewDecoder(res.Body).Decode(destino); err != nil {
			return 0, fmt.Errorf("resposta de %s %s: %w", metodo, rota, err)
		}
	}
	return res.StatusCode, nil
}
