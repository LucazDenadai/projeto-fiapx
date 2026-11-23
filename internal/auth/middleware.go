package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type chaveUsuario struct{}

// UsuarioDe devolve o id do Usuário autenticado, posto no contexto por Exigir.
func UsuarioDe(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(chaveUsuario{}).(string)
	return id, ok && id != ""
}

// Exigir só deixa passar requisições com "Authorization: Bearer <token>"
// válido e põe o id do Usuário no contexto. As demais recebem 401.
func (s *Servico) Exigir(proximo http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esquema, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		id, err := s.emissor.validar(token)
		// O esquema não diferencia maiúsculas (RFC 7235).
		if !strings.EqualFold(esquema, "Bearer") || err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"erro": errTokenInvalido.Error()})
			return
		}
		proximo.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), chaveUsuario{}, id)))
	})
}
