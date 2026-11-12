# FIAP X — Processamento de Vídeos

Hackathon POSTECH SOAT (Fase 5). Envie um vídeo, o sistema extrai os frames e devolve um `.zip` para download.

> Status: **levantamento de requisitos e arquitetura.** Ainda sem código de aplicação.

## Índice

### Requisitos
| Documento | Descrição |
|---|---|
| [Enunciado](docs/enunciado.md) ([PDF](docs/enunciado.pdf)) | Requisitos oficiais do hackathon |
| [Matriz de requisitos](docs/matriz-requisitos.md) | Requisito × problema do projeto base × decisão pendente |

### Arquitetura (ADRs)
Em `docs/adr/`. Cada um resolve ambiguidades A1–A12 da [matriz](docs/matriz-requisitos.md#4-ambiguidades-a-resolver-viram-adrs); progresso no [mapa de decisões](https://github.com/LucazDenadai/projeto-fiapx/issues/1).

| ADR | Título | Status | Resolve |
|---|---|---|---|
| [0001](docs/adr/0001-api-worker-notifier.md) | Três processos: API, Worker e Notifier | accepted | A1 (RF01, RF04, RT02) |

### Diagramas
Ficarão em `docs/diagramas/` (nenhum ainda).

### Outros
| Documento | Descrição |
|---|---|
| [Glossário](GLOSSARY.md) | Vocabulário do domínio (Usuário, Vídeo, Processamento, Zip de frames) |
| [`legacy/`](legacy/) | Projeto base entregue no enunciado (referência do "antes") |
| [Handoff](docs/HANDOFF-fase5.md) | Contexto inicial da fase |
| [Configuração das skills](docs/agents/) | Issue tracker, labels de triagem e layout de domínio usados pelas skills |
| [CLAUDE.md](CLAUDE.md) | Regras e fluxo de trabalho com agentes |
