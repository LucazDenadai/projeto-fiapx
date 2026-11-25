# FIAP X — Processamento de Vídeos

Hackathon POSTECH SOAT (Fase 5). Envie um vídeo, o sistema extrai os frames e devolve um `.zip` para download.

> Status: cadastro, login, envio, Processamento, download do Zip de frames, retry com DLQ, e-mail de Falha definitiva, retenção e escala prontos. Faltam a observabilidade, o CD e os diagramas ([issues abertas](https://github.com/LucazDenadai/projeto-fiapx/issues)).

## Como rodar

Requisitos: Docker Desktop (Compose v2) e, para os testes, Go e ffmpeg no PATH.

```sh
cp .env.example .env          # troque as senhas e o JWT_SECRET
docker compose up -d --build  # migrate roda antes de API e Worker
docker compose ps             # api, worker e notifier ficam healthy
```

Só a API publica porta (`API_PORT`, padrão 8080). A porta interna `9090` de cada processo (`/healthz`, `/readyz`) fica na rede do Compose.

Para usar a API, importe no Postman a coleção e o ambiente de [`docs/api/`](docs/api/) e rode as requisições em ordem: o **Login** salva o token, que vai sozinho nas rotas de Vídeo. O **Enviar vídeo** usa `docs/api/video.mp4`, um vídeo de 5 s versionado no repositório. O **Aguardar conclusão** repete a consulta até o Vídeo ficar `concluido`, e o **Baixar zip** traz o Zip de frames; no Postman, rode a coleção pelo Collection Runner. Quando um Vídeo falha, o Notifier manda o e-mail de aviso; no Compose ele chega no Mailpit, em http://localhost:8025.

### Escala

O Compose sobe 2 Workers, cada um com um ffmpeg por vez e limite de CPU e memória. Para mais vazão, mais réplicas:

```sh
docker compose up -d --scale worker=4
go run ./cmd/pico              # 20 envios em paralelo, com o progresso até o fim
```

O pico usa a API como qualquer cliente: cadastra (ou reusa) Usuários `pico-N@fiapx.local`, 5 Vídeos para cada por causa da cota. Opções em `go run ./cmd/pico -h`.

Testes: `go test -race ./...` sobe Postgres, RabbitMQ e Redis descartáveis com testcontainers (Docker precisa estar rodando); `go test -short ./...` pula a integração. O CI fica em [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

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
| [0002](docs/adr/0002-rabbitmq-mensageria.md) | RabbitMQ com quorum queues para Processamento e eventos | accepted | A2 (RF01, RF02, S02) |
| [0003](docs/adr/0003-transactional-outbox.md) | Transactional Outbox para publicar no RabbitMQ | accepted | A3 (RF02) |
| [0004](docs/adr/0004-retry-e-dlq.md) | Política de retry e DLQ do Processamento e da notificação | accepted | Retry/DLQ (RF02, RF05) |
| [0005](docs/adr/0005-volume-arquivos.md) | Volume nomeado do Docker para vídeos e zips | accepted | A4 (RT01, RT02) |
| [0006](docs/adr/0006-envio-e-download-pela-api.md) | Envio e download dos arquivos pela API, em streaming | accepted | A11 (RF01, RT02) |
| [0007](docs/adr/0007-autenticacao-jwt-na-api.md) | Cadastro e login por e-mail e senha na própria API, com JWT | accepted | A5 (RF03) |
| [0008](docs/adr/0008-notificacao-por-email.md) | Notificação de Falha definitiva por e-mail (SMTP, Mailpit no Compose) | accepted | A6 (RF05) |
| [0009](docs/adr/0009-redis-limite-de-tentativas.md) | Redis só para o limite de tentativas de login e cadastro | accepted | A7 (RF04, S03) |
| [0010](docs/adr/0010-observabilidade-prometheus-grafana.md) | Observabilidade com Prometheus + Grafana, logs JSON no stdout | accepted | A8 (S04, E04) |
| [0011](docs/adr/0011-escala-e-limites.md) | Escala pelo Worker e limites que protegem CPU, memória e disco | accepted | A9 (RT02, RF01) |
| [0012](docs/adr/0012-ci-cd-github-actions-ghcr.md) | CI/CD: GitHub Actions, imagens no GHCR e deploy efêmero no runner | accepted | A10 (RT05) |
| [0013](docs/adr/0013-sem-frontend-colecao-postman.md) | Sem frontend: a API é usada por uma coleção do Postman versionada | accepted | A12 (E01, E04) |
| [0014](docs/adr/0014-ciclo-de-vida-do-video.md) | Ciclo de vida do Vídeo: quatro estados, transições só para a frente | accepted | Ciclo de vida (RF04, RF02) |
| [0015](docs/adr/0015-retencao-e-limpeza.md) | Retenção: original apagado ao terminar, zip por 7 dias, limpador no Worker | accepted | Retenção (RT01, RT02) |
| [0016](docs/adr/0016-testes-de-integracao-e-migrations.md) | Testes de integração com testcontainers-go e migrations com goose | accepted | Testes e migrations (RT04, E02) |

### Diagramas
Ficarão em `docs/diagramas/` (nenhum ainda).

### Outros
| Documento | Descrição |
|---|---|
| [Coleção do Postman](docs/api/fiapx.postman_collection.json) ([ambiente](docs/api/fiapx.postman_environment.json)) | Interface e documentação da API: rotas, códigos de resposta e token salvo pelo login (ADR-0013) |
| [Glossário](GLOSSARY.md) | Vocabulário do domínio (Usuário, Vídeo, Processamento, Zip de frames) |
| [`legacy/`](legacy/) | Projeto base entregue no enunciado (referência do "antes") |
| [Handoff](docs/HANDOFF-fase5.md) | Contexto inicial da fase |
| [Configuração das skills](docs/agents/) | Issue tracker, labels de triagem e layout de domínio usados pelas skills |
| [CLAUDE.md](CLAUDE.md) | Regras e fluxo de trabalho com agentes |
