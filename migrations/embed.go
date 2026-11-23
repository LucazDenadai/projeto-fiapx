// Package migrations embute os SQL do schema, aplicados pelo goose (ADR-0016).
// São só para a frente: para corrigir, escreva uma migration nova. Toda
// migration precisa ser compatível com a API e o Worker em execução.
package migrations

import "embed"

// FS contém os arquivos NNNNN_nome.sql deste diretório.
//
//go:embed *.sql
var FS embed.FS
