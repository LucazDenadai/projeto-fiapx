package api_test

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	"github.com/LucazDenadai/projeto-fiapx/internal/api"
	"github.com/LucazDenadai/projeto-fiapx/internal/integracao"
	"github.com/LucazDenadai/projeto-fiapx/internal/limite"
)

func TestCadastro_GuardaEmailEmMinusculasESoOHash(t *testing.T) {
	c, pool := subirAPI(t)

	if r := c.cadastrar("Ana@Ex.com", "Forte#123"); r.status != http.StatusCreated {
		t.Fatalf("status = %d, quero 201 (corpo %v)", r.status, r.corpo)
	}

	// O critério de aceite é sobre o que fica gravado, que a API não expõe.
	var hash string
	err := pool.QueryRow(t.Context(), `SELECT senha_hash FROM usuarios WHERE email = 'ana@ex.com'`).Scan(&hash)
	if err != nil {
		t.Fatalf("buscar usuário ana@ex.com: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Errorf("hash = %q, quero formato PHC argon2id", hash)
	}
	if strings.Contains(hash, "Forte#123") {
		t.Error("a senha não pode ser guardada em claro")
	}
}

func TestCadastro_SenhaForaDaRegraDiz400ComOQueFalta(t *testing.T) {
	// Mais tentativas que o limite de cadastro por IP permite.
	regras := limite.RegrasPadrao()
	regras.CadastroPorIP.Max = 100
	c, _ := subirAPIComRegras(t, regras)
	casos := []struct{ senha, falta string }{
		{"Curta#1", "de 8 a 128 caracteres"},
		{"semmaius#1", "uma letra maiúscula"},
		{"SEMMINUS#1", "uma letra minúscula"},
		{"SemNumero#", "um número"},
		{"SemEspec1", "um caractere especial"},
		{"Longa#1" + strings.Repeat("a", 122), "de 8 a 128 caracteres"},
	}
	for _, caso := range casos {
		r := c.cadastrar("regra@ex.com", caso.senha)
		if r.status != http.StatusBadRequest {
			t.Errorf("senha %q: status = %d, quero 400", caso.senha, r.status)
			continue
		}
		if msg, _ := r.corpo["erro"].(string); msg != "a senha precisa ter "+caso.falta {
			t.Errorf("senha %q: erro = %q, quero só %q", caso.senha, msg, caso.falta)
		}
	}
}

func TestCadastro_EmailJaCadastradoEmOutraCaixaDiz409(t *testing.T) {
	c, _ := subirAPI(t)
	if r := c.cadastrar("bia@ex.com", "Forte#123"); r.status != http.StatusCreated {
		t.Fatalf("primeiro cadastro: status = %d", r.status)
	}
	r := c.cadastrar("BIA@ex.com", "Outra#456")
	if r.status != http.StatusConflict || r.corpo["erro"] != "e-mail já cadastrado" {
		t.Errorf("status %d corpo %v, quero 409 \"e-mail já cadastrado\"", r.status, r.corpo)
	}
}

func TestLogin_ValidoDevolveTokenQueExpiraEmUmaHora(t *testing.T) {
	c, _ := subirAPI(t)
	c.cadastrar("caio@ex.com", "Forte#123")

	r := c.entrar("Caio@Ex.com", "Forte#123")
	if r.status != http.StatusOK {
		t.Fatalf("status = %d, quero 200 (corpo %v)", r.status, r.corpo)
	}
	if token, _ := r.corpo["token"].(string); token == "" {
		t.Error("resposta sem token")
	}
	texto, _ := r.corpo["expira_em"].(string)
	expira, err := time.Parse(time.RFC3339, texto)
	if err != nil {
		t.Fatalf("expira_em = %q, quero RFC 3339: %v", texto, err)
	}
	if d := time.Until(expira); d < 59*time.Minute || d > time.Hour+time.Minute {
		t.Errorf("expira_em daqui a %v, quero uma hora", d)
	}
}

func TestLogin_EmailInexistenteESenhaErradaDaoOMesmo401(t *testing.T) {
	c, _ := subirAPI(t)
	c.cadastrar("duda@ex.com", "Forte#123")

	for nome, r := range map[string]resposta{
		"inexistente":  c.entrar("ninguem@ex.com", "Forte#123"),
		"senha errada": c.entrar("duda@ex.com", "Errada#123"),
	} {
		if r.status != http.StatusUnauthorized || r.corpo["erro"] != "credenciais inválidas" {
			t.Errorf("%s: status %d corpo %v, quero 401 \"credenciais inválidas\"", nome, r.status, r.corpo)
		}
	}
}

func TestRotaDeVideo_RecusaTokenAusenteExpiradoOuComOutroAlgoritmo(t *testing.T) {
	c, _ := subirAPI(t)
	agora := time.Now()
	valido := jwt.RegisteredClaims{Subject: "00000000-0000-0000-0000-000000000001",
		IssuedAt: jwt.NewNumericDate(agora), ExpiresAt: jwt.NewNumericDate(agora.Add(time.Hour))}
	expirado := jwt.RegisteredClaims{Subject: valido.Subject,
		IssuedAt: jwt.NewNumericDate(agora.Add(-2 * time.Hour)), ExpiresAt: jwt.NewNumericDate(agora.Add(-time.Hour))}
	semExp := jwt.RegisteredClaims{Subject: valido.Subject, IssuedAt: jwt.NewNumericDate(agora)}
	assinar := func(m jwt.SigningMethod, claims jwt.Claims, chave any) string {
		tok, err := jwt.NewWithClaims(m, claims).SignedString(chave)
		if err != nil {
			t.Fatalf("assinar: %v", err)
		}
		return tok
	}
	casos := map[string]string{
		"sem token":     "",
		"lixo":          "nao-e-um-jwt",
		"expirado":      assinar(jwt.SigningMethodHS256, expirado, []byte(segredoTeste)),
		"sem exp":       assinar(jwt.SigningMethodHS256, semExp, []byte(segredoTeste)),
		"HS384":         assinar(jwt.SigningMethodHS384, valido, []byte(segredoTeste)),
		"alg none":      assinar(jwt.SigningMethodNone, valido, jwt.UnsafeAllowNoneSignatureType),
		"outro segredo": assinar(jwt.SigningMethodHS256, valido, []byte("outro-segredo-com-pelo-menos-32-bytes!!")),
	}
	for nome, token := range casos {
		if r := c.fazer(http.MethodGet, "/videos", token, nil); r.status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, quero 401", nome, r.status)
		}
	}
}

func TestRotaDeVideo_AceitaOTokenDoLogin(t *testing.T) {
	c, _ := subirAPI(t)
	c.cadastrar("eva@ex.com", "Forte#123")
	token, _ := c.entrar("eva@ex.com", "Forte#123").corpo["token"].(string)

	// O esquema do header não diferencia maiúsculas (RFC 7235).
	for _, esquema := range []string{"Bearer", "bearer"} {
		if r := c.fazerComEsquema(http.MethodGet, "/videos", esquema, token, nil); r.status == http.StatusUnauthorized {
			t.Errorf("esquema %q: status = 401 com token válido", esquema)
		}
	}
}

func TestNovo_ExigeSegredoDoJWT(t *testing.T) {
	pool := integracao.Postgres(t)
	log := slog.New(slog.DiscardHandler)
	tentativas := novasTentativas(t, &redis.Options{}, limite.Config{Regras: limite.RegrasPadrao()})
	for nome, segredo := range map[string][]byte{"ausente": nil, "curto": []byte("curto")} {
		_, err := api.Novo(api.Config{Pool: pool, SegredoJWT: segredo, Log: log, Tentativas: tentativas, DirDados: t.TempDir()})
		if err == nil {
			t.Errorf("segredo %s: api.Novo deveria falhar", nome)
		}
	}
}
