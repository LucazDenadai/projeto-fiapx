package observabilidade_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/LucazDenadai/projeto-fiapx/internal/observabilidade"
)

func TestNovoLogger_CadaLinhaEhJSONComOServico(t *testing.T) {
	var saida bytes.Buffer
	log := observabilidade.NovoLogger("worker", &saida)

	log.Info("subiu", "porta", ":9090")
	log.Error("falhou", "erro", "boom")

	linhas := 0
	sc := bufio.NewScanner(&saida)
	for sc.Scan() {
		linhas++
		var campos map[string]any
		if err := json.Unmarshal(sc.Bytes(), &campos); err != nil {
			t.Fatalf("linha %d não é JSON: %q: %v", linhas, sc.Text(), err)
		}
		if campos["servico"] != "worker" {
			t.Errorf("linha %d sem servico=worker: %v", linhas, campos)
		}
	}
	if linhas != 2 {
		t.Fatalf("esperava 2 linhas de log, veio %d", linhas)
	}
}
