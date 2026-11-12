# Handoff — Fase 5 (Hackathon FIAP X)

> **Atualização 2026-11-11 — decisões que substituem partes abaixo:**
> - Linguagem: **Go** (fecha a dúvida .NET vs Go).
> - Projeto **todo novo**: nada reaproveitado da Fase 4 (Kind, OTel, repos, `tech-challenge-docs`). Seções "reaproveitar" abaixo estão obsoletas.
> - Plataforma: **Docker + Docker Compose** (o PDF aceita; não exige nuvem). Dockerfile do enunciado refeito com boas práticas.
> - Docs vivem **neste repo** (`docs/`), ADRs a partir de 0001.
> - Fluxo de trabalho: skills do **AI Hero (Matt Pocock)** em `.claude/skills/` — revoga o "pular Matt Pocock". Mapa de skills no `CLAUDE.md`.
> - Tickets em **GitHub Issues** (substituem os "cards"). Individual, prazo folgado.
> - Matriz de requisitos pronta em `docs/matriz-requisitos.md`.

## Contexto
- Pós FIAP SOAT. Fases 1–4 = Oficina Mecânica (repos Tech-challenge, tech-challenge-os, -operacoes, -billing, -lambda, -infra-k8s, -infra-db; docs em D:\Dev\tech-challenge-docs).
- Fase 5 é um domínio NOVO: Hackathon "FIAP X", processamento de vídeos (upload de vídeo → extrair frames → zip para download).
- Enunciado: D:\Dev\tech-challenge-docs\POSTECH - SOAT - Fase 5 - Hacka.pdf

## Requisitos do enunciado
- Funcionais: processar vários vídeos ao mesmo tempo; não perder requisição em picos; proteção por usuário e senha; listagem de status dos vídeos do usuário; notificação em caso de erro (e-mail ou outro).
- Técnicos: persistência; arquitetura escalável; versionado no GitHub; testes; CI/CD.
- Stack recomendada: Docker + K8s ou Compose; RabbitMQ/Kafka; PostgreSQL + Redis; Prometheus + Grafana/ELK; GitHub Actions.
- Entregáveis: documentação da arquitetura; script de criação do banco e dos recursos; links GitHub; vídeo de no máximo 10 min (documentação, arquitetura, projeto funcionando).

## Decisões de fluxo de trabalho já tomadas
- Repetir o processo da Fase 4 (matriz de requisitos → ADRs → cards com Gherkin → implementação → evidências), não adotar um processo novo.
- Skills de terceiros: pular Addy Osmani e Matt Pocock (duplicam o processo e os comandos /code-review e /simplify). Superpowers só para TDD e debugging na implementação (o planejamento dele conflita com o formato dos docs). Trail of Bits é opcional; /security-review talvez baste (superfície de upload e zip). Ler o SKILL.md antes de instalar qualquer uma.
- Preferir skills locais do projeto: /novo-adr, /novo-card, /matriz-requisitos.
- Adicionar ao CLAUDE.md uma "Regra 4": nada de código antes de a arquitetura ser aprovada; não assumir requisitos; separar obrigatório de opcional.
- Domínio leve (Vídeo/Processamento, Usuário, Notificação), sem Event Storming pesado.
- Os repos da Fase 4 servem só como base reaproveitável (Rabbit, Kind, stack OTel ADR-021/022, CI por serviço).

## Fluxo proposto
1. Matriz de requisitos da Fase 5 + análise do projeto base + ambiguidades (fila durável/outbox? meio de notificação? auth própria ou reaproveitar a Lambda?).
2. Inventário do que se reaproveita dos repos existentes.
3. Modelo de domínio leve.
4. ADRs: limites de serviço, armazenamento (S3/MinIO), fila e escalonamento do worker (HPA/KEDA), auth, notificação, plataforma (Kind vs AWS). Cada um aprovado pelo usuário.
5. Cards.
6. Implementação com TDD, /code-review e /security-review; evidências; roteiro do vídeo de 10 min.

## Perguntas em aberto
1. Código ou link do projeto base (o link do PDF não veio no texto).
2. Prazo e se é individual ou em grupo.
3. Kind local (como no ADR-021) ou AWS.
4. Começar pelas skills locais + Regra 4, ou direto pela matriz de requisitos.

## Análise do projeto base (C:\Users\lucaz\Downloads\projeto-fiapx)
Go 1.21 + Gin, tudo em um único main.go, extração de frames com ffmpeg (fps=1) e zip. Não é um repositório git. A pasta __MACOSX é lixo do zip.
Problemas, que viram os "porquês" dos ADRs:
- Processamento síncrono dentro do request HTTP: timeout em vídeo grande, sem paralelismo controlado, a requisição se perde se o pod cair (viola "não perder requisição").
- Sem autenticação, CORS `*`, sem usuário: /api/status lista os zips de TODOS.
- Estado só no filesystem local (uploads/outputs/temp): sem persistência, não escala horizontalmente.
- /download/:filename sem checar dono; o nome do arquivo vem do usuário; validação só por extensão; sem limite de tamanho.
- Erro do ffmpeg vaza para o cliente; sem notificação; sem retry nem DLQ.
- Sem testes, sem CI, sem observabilidade (só fmt.Println).
- Dockerfile roda `go run` com toolchain completo e `COPY . .` (o próprio arquivo diz "como NÃO fazer").
Decisão em aberto: reescrever em .NET (stack do aluno, reaproveita padrões das fases 1–4) ou manter Go no worker (ffmpeg é agnóstico de linguagem).
