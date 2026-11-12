# FIAP X — processamento de vídeos (Hackathon SOAT Fase 5)

Usuário envia vídeo → sistema extrai frames com ffmpeg → usuário baixa um zip. Projeto individual, novo (nada da Fase 4). Go + Docker Compose; o resto da stack sai dos ADRs.

- Requisitos: [`docs/enunciado.md`](docs/enunciado.md) (fonte) e [`docs/matriz-requisitos.md`](docs/matriz-requisitos.md) (interpretação, ambiguidades A1–A12).
- Decisões: `docs/adr/`. Vocabulário do domínio: `GLOSSARY.md` (criado sob demanda).
- `legacy/` é o projeto base do enunciado: referência do "antes", somente leitura. Nunca importar nem evoluir esse código.

## Regras

1. **Arquitetura antes de código.** Código de aplicação só depois que os ADRs que ele toca estiverem `accepted` pelo usuário. Requisito ambíguo vira pergunta, não suposição. Separe sempre obrigatório (enunciado diz "deve") de opcional.
2. **Docs coerentes.** Antes de criar/editar ADR, procure conflito com os existentes. ADR substituído recebe `superseded by ADR-NNNN`; nunca dois ADRs aceitos se contradizendo.
3. **ADR é entregável avaliado.** Além do formato da skill `domain-modeling`, todo ADR tem `Status`, `Opções consideradas` e `Consequências`, e cita os IDs da matriz (RF/RT/A) que resolve. Em português.
4. **Tickets** (GitHub Issues) têm critérios de aceite em Gherkin (`Dado/Quando/Então`) e declaram bloqueios.
5. **Índice.** Todo doc novo em `docs/` entra no índice do [`README.md`](README.md).

## Qual skill para cada parte

| Momento | Skill |
|---|---|
| Não sei qual usar | `/ask-matt` |
| Mapear decisões grandes (greenfield, várias sessões) | `/wayfinder` |
| Afinar uma ideia / resolver ambiguidade → ADR + glossário | `/grill-with-docs` |
| Pergunta só respondível rodando código | `/prototype` (via `/handoff`) |
| Pergunta para alguém de fora (ex.: professor) | `/to-questionnaire` |
| Spec e quebra em tickets | `/to-spec` → `/to-tickets` |
| Implementar um ticket / a spec inteira com subagentes | `/implement` / `/implement-spec` (ambos dirigem `/tdd`) |
| Bug difícil | `/diagnosing-bugs` |
| Revisão | `/code-review`; e `/security-review` em toda mudança de upload, download, zip, auth ou execução do ffmpeg |
| Fim de sessão | `/retro`, `/handoff` |

## Go

- Layout `cmd/<serviço>/` + `internal/<conceito do domínio>/`; pacotes nomeados pelo que fazem.
- Interface declarada no pacote que a consome; construtores recebem dependências.
- Toda I/O recebe `context.Context`; ffmpeg sempre com `exec.CommandContext` e timeout.
- Erros embrulhados com `%w`; ao cliente só mensagem pública, detalhe vai para o log (`log/slog`, JSON).
- Pronto = `gofmt`, `go vet ./...` e `go test -race ./...` verdes.

## Agent skills

### Issue tracker

GitHub Issues em LucazDenadai/projeto-fiapx, via `gh`, com sub-issues e bloqueios nativos. Ver `docs/agents/issue-tracker.md`.

### Triage labels

As cinco labels padrão: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. Ver `docs/agents/triage-labels.md`.

### Domain docs

Contexto único: `GLOSSARY.md` na raiz e `docs/adr/`. Ver `docs/agents/domain.md`.
