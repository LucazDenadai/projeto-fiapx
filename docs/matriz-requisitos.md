# Matriz de requisitos — Fase 5

Cada requisito do [enunciado](enunciado.md) cruzado com o estado do projeto base ([`legacy/main.go`](../legacy/main.go)) e com a decisão que ele exige.

- **Obrigatório**: o enunciado exige ("deve", entregável).
- **Recomendado**: aparece como "stack recomendada" ou "pode"; entra se houver justificativa.
- **ADR**: decisão a tomar; numeração definitiva só quando o ADR for aceito em [`adr/`](adr/).
- Status de todas as linhas: **aberto** até as ambiguidades (seção 4) serem resolvidas.

## 1. Requisitos funcionais

| ID | Requisito (enunciado) | Nível | Problema no projeto base | Como atender (direção, não decisão) | Decisão pendente |
|---|---|---|---|---|---|
| RF01 | Processar mais de um vídeo ao mesmo tempo | Obrigatório | Processamento síncrono dentro do request: `handleVideoUpload` chama `processVideo` direto (`main.go:117`). Um vídeo longo segura a conexão HTTP; concorrência só existe por acaso (goroutines do Gin), sem limite nem controle. | Upload só registra o Vídeo e enfileira um Job; workers consomem a fila em paralelo, com concorrência configurável por worker. | A1, A2 |
| RF02 | Em picos, não perder requisição | Obrigatório | Sem fila nem estado persistido: se o processo cai durante o ffmpeg, o vídeo e o pedido se perdem (`uploads/` local, sem registro). | Fila durável com ack manual após conclusão; mensagem persistente; retry com DLQ; garantia de consistência entre "gravar Vídeo" e "publicar Job". | A2, A3 |
| RF03 | Protegido por usuário e senha | Obrigatório | Nenhuma autenticação; CORS `*` (`main.go:36`). | Cadastro/login com senha em hash forte; todas as rotas de vídeo exigem identidade. | A5 |
| RF04 | Listagem de status dos vídeos de um usuário | Obrigatório | `/api/status` lista os zips de **todos** pelo filesystem (`main.go:254`); não existe "status" (só arquivos prontos), nem dono. | Vídeo persistido com dono e ciclo de status (ex.: recebido → processando → concluído / falhou); endpoint filtra pelo usuário autenticado. | A1, A7 |
| RF05 | Em caso de erro, usuário pode ser notificado (e-mail ou outro) | Obrigatório (o "pode" se refere ao meio) | Não há notificação; o erro bruto do ffmpeg vaza na resposta (`main.go:146`). | Evento de falha publicado quando o Job esgota tentativas; consumidor de notificação envia e-mail; mensagem ao usuário sem detalhe interno. | A6 |

## 2. Requisitos técnicos

| ID | Requisito | Nível | Problema no projeto base | Como atender | Decisão pendente |
|---|---|---|---|---|---|
| RT01 | Persistir os dados | Obrigatório | Estado só em pastas locais (`uploads/`, `outputs/`, `temp/`); nenhum banco. | PostgreSQL para usuários/vídeos/status; armazenamento de objetos para vídeos e zips. | A4 |
| RT02 | Arquitetura que permita escalar | Obrigatório | Um processo, disco local, sem separação entre receber e processar; não roda em duas réplicas. | API e worker separados e sem estado local; worker escala horizontalmente por consumidores competindo na fila (`docker compose up --scale worker=N`). | A1, A9 |
| RT03 | Versionado no GitHub | Obrigatório | Não era repositório git. | Repo no GitHub; tickets em GitHub Issues; PRs. | — |
| RT04 | Testes que garantam qualidade | Obrigatório | Nenhum teste. | TDD (unitários no domínio, integração com Postgres/fila/ffmpeg reais via containers); `go test -race`. | — |
| RT05 | CI/CD | Obrigatório | Nenhum. | GitHub Actions: lint, testes, build de imagens; CD = publicar imagens em registry. Alvo de deploy a definir. | A10 |

## 3. Entregáveis e stack

| ID | Item | Nível | Observação |
|---|---|---|---|
| E01 | Documentação da arquitetura | Obrigatório | ADRs em `docs/adr/` + diagramas em `docs/diagramas/` + README. |
| E02 | Script de criação do banco e de outros recursos | Obrigatório | Migrations SQL versionadas + `docker-compose.yml` (fila, buckets, banco) como script de recursos. |
| E03 | Link do GitHub do(s) projeto(s) | Obrigatório | Depende de RT03. |
| E04 | Vídeo de até 10 min (docs, arquitetura, projeto funcionando) | Obrigatório | Roteiro no fim; mostrar escala e "não perder requisição" ao vivo. |
| S01 | Docker + Kubernetes **ou** Compose | Recomendado | **Decidido: Docker + Compose**, partindo do Dockerfile do enunciado refeito com boas práticas (ver seção 5). |
| S02 | RabbitMQ, Kafka ou similar | Recomendado | A2. |
| S03 | PostgreSQL + Redis (cache) | Recomendado | Postgres praticamente certo; Redis só com uso real (A7). |
| S04 | Prometheus + Grafana, ELK… | Recomendado | A8. |
| S05 | GitHub Actions | Recomendado | Adotado (RT05). |

## 4. Ambiguidades a resolver (viram ADRs)

Resolvidas uma a uma via `/wayfinder` ou `/grill-with-docs`, com aprovação do usuário.

| # | Pergunta | Opções principais | Afeta |
|---|---|---|---|
| A1 | Limites de serviço: quais processos existem? | (a) API + Worker + Notifier; (b) API + Worker, notificação dentro do worker; (c) separar Auth em serviço próprio | RF01, RF04, RT02 |
| A2 | Mensageria | RabbitMQ (filas de trabalho, DLQ nativa, ack por mensagem) vs Kafka (log, replay, mais pesado) | RF01, RF02 |
| A3 | Consistência "gravar Vídeo + publicar Job" | Publicar após commit (simples, janela de perda) vs Transactional Outbox (sem perda, mais código) vs reconciliador que reenfileira órfãos | RF02 |
| A4 | Onde ficam vídeo e zip | MinIO (S3-compatível, escala, URL pré-assinada) vs volume compartilhado | RT01, RT02 |
| A5 | Autenticação | JWT emitido pela própria API (bcrypt/argon2 + Postgres) vs IdP (Keycloak) | RF03 |
| A6 | Meio de notificação | E-mail via SMTP de desenvolvimento (Mailpit) vs webhook vs outro | RF05 |
| A7 | Redis: usar para quê? | Cache de status / rate limit / nada (não usar) | RF04, S03 |
| A8 | Observabilidade | Prometheus + Grafana (métricas de fila e jobs) / + logs centralizados / só logs `slog` | S04, E04 |
| A9 | Como demonstrar escala e limitar recursos | `--scale worker=N` + concorrência por worker; limites de tamanho/duração do vídeo | RT02, RF01 |
| A10 | O que é o "CD" num projeto Compose local | Publicar imagens no GHCR / deploy em VM / só build | RT05 |
| A11 | Upload: pela API ou direto no storage | Multipart pela API (simples) vs URL pré-assinada direto no MinIO (API não trafega bytes) | RF01, RT02 |
| A12 | Frontend | Manter página HTML simples servida pela API vs só API + coleção Postman | E04 |

## 5. Achados de segurança e qualidade no projeto base

Não estão no enunciado, mas "boas práticas" é o critério do desafio; cada um vira critério de aceite em algum ticket.

| # | Achado | Onde |
|---|---|---|
| Q01 | `outputs/` e `uploads/` servidos como estáticos públicos: qualquer um baixa qualquer zip, contornando até o `/download` | `main.go:48-49` |
| Q02 | Download sem checagem de dono; nome do arquivo vem do path | `main.go:236-238` |
| Q03 | Nome do arquivo salvo derivado do nome enviado pelo cliente | `main.go:95` |
| Q04 | Validação só pela extensão; sem checar conteúdo (magic bytes / `ffprobe`) | `main.go:86` |
| Q05 | Sem limite de tamanho de upload nem de duração/quantidade de frames (risco de encher disco) | `main.go:76-108` |
| Q06 | Erro interno do ffmpeg devolvido ao cliente | `main.go:146` |
| Q07 | `exec.Command` sem timeout/`context` (ffmpeg pode travar para sempre) | `main.go:135` |
| Q08 | Logs com `fmt.Println`, sem estrutura nem correlação | `main.go:62,127,158,171` |
| Q09 | Dockerfile: `go run` em produção, toolchain inteiro na imagem, `COPY . .`, `go mod tidy` no build, roda como root | `legacy/Dockerfile:4-25` |
| Q10 | Sem graceful shutdown, health check ou configuração por ambiente | `main.go:65` |

## 6. Fora de escopo (até decisão em contrário)
- Kubernetes, nuvem pública e Terraform: plataforma decidida é Docker Compose.
- Reaproveitar artefatos da Fase 4: projeto novo.
