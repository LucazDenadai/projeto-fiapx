---
status: accepted
resolve: S04, E04, A8
---

# ADR-0010 — Observabilidade com Prometheus + Grafana, logs JSON no stdout

## Contexto

O projeto base só tem `fmt.Println` (Q08). A stack recomendada sugere "Prometheus + Grafana, ELK Stack ou algo de preferência" (S04). O vídeo de entrega (E04) precisa mostrar o projeto funcionando: escala e "não perder requisição". O `CLAUDE.md` já fixa os logs em `log/slog` JSON, com o detalhe dos erros só no log. O [ADR-0004](0004-retry-e-dlq.md) acrescenta o `correlation_id` nesses logs. Os processos e as filas vêm dos ADRs [0001](0001-api-worker-notifier.md), [0002](0002-rabbitmq-mensageria.md) e [0004](0004-retry-e-dlq.md).

## Decisão

**Métricas no Prometheus, painel no Grafana, logs JSON no stdout. Sem logs centralizados e sem tracing.**

- **Prometheus** coleta, a cada 15 s:
  - o `/metrics` da API, do Worker e do Notifier, que usam `prometheus/client_golang`;
  - as métricas nativas do RabbitMQ, pelo plugin `rabbitmq_prometheus`.
- **Grafana** sobe com o datasource e **um dashboard** já configurados por provisioning, em arquivos versionados no repositório. Ele abre pronto, sem configuração manual. É o único serviço de observabilidade exposto fora do Compose.
- **Métricas:**
  - **API:** requisições por rota, status e latência (histograma); Vídeos aceitos; respostas `429` e falhas do Redis ([ADR-0009](0009-redis-limite-de-tentativas.md)).
  - **Worker:** Processamentos por resultado (`concluido` e `falhou` por Motivo da falha); duração do Processamento e do ffmpeg (histogramas); Processamentos em andamento; linhas pendentes da outbox e idade da mais antiga ([ADR-0003](0003-transactional-outbox.md)).
  - **Notifier:** Notificações enviadas e falhas de SMTP ([ADR-0008](0008-notificacao-por-email.md)).
  - **RabbitMQ:** mensagens prontas e não confirmadas em `processamento`, `processamento.dlq`, `notificacao` e `notificacao.dlq`.

  Os rótulos são de cardinalidade baixa: rota como padrão (`/videos/{id}`), nunca o id; Motivo da falha como código. Nada de e-mail, `video_id` ou `correlation_id` em rótulo.
- **Porta interna separada** em todos os processos. Ela serve `/metrics`, `/healthz` (processo vivo) e `/readyz` (dependências respondem: Postgres, RabbitMQ e, na API, o volume). O Redis fica fora do `/readyz`, porque o sistema funciona sem ele (fail-open). A porta pública da API serve só as rotas de negócio.
- **Health checks no Compose:** `healthcheck` usando o `/readyz`, e `depends_on: service_healthy` para ordenar a subida.
- **Logs:** `slog` JSON no stdout, com `correlation_id`, `video_id` e serviço em cada linha do fluxo de um Vídeo. Lidos com `docker compose logs`. Senha, token e conteúdo de arquivo nunca vão ao log.
- **Sem alertas** (Alertmanager): o dashboard é o meio de observar.
- **No vídeo de entrega (E04)**, o dashboard é o cenário-guia:
  1. um script dispara um pico de envios e a fila `processamento` sobe;
  2. `docker compose up --scale worker=3` faz a fila escoar mais rápido e a vazão subir;
  3. um vídeo inválido termina em `falhou` e o e-mail aparece no Mailpit.

## Opções consideradas

- **Logs centralizados (Loki + Promtail, ou ELK)**: rejeitado. São 2 ou 3 containers a mais, e o ELK sozinho pesa mais que o resto da stack. Num Compose de um host, `docker compose logs` com filtro pelo `correlation_id` basta. Fica como evolução.
- **Tracing distribuído (OpenTelemetry + Jaeger ou Tempo)**: rejeitado. O `correlation_id` já liga API → outbox → fila → Worker → Notifier nos logs. Tracing exigiria um coletor, um backend e instrumentação em todo ponto de I/O.
- **Só logs, sem métricas**: rejeitado. Não mostra a fila subindo e escoando nem a vazão com mais Workers, que é a evidência de escala (RT02) que o vídeo precisa.
- **`/metrics` na porta pública da API**: rejeitado. Exporia números internos (volume de tráfego, falhas) a qualquer cliente.
- **Alertmanager com regras**: rejeitado. Ninguém recebe alerta num projeto de demonstração; as regras seriam só decorativas.
- **Dashboard montado pela interface do Grafana**: rejeitado. Some ao recriar o container e não fica versionado no repositório.

## Consequências

- O Compose ganha `prometheus` e `grafana`. O Grafana fica exposto, com credencial vinda do `.env`.
- Cada processo abre duas portas, a pública (só na API) e a interna. Os testes cobrem `/healthz` e `/readyz`, inclusive o `/readyz` falhando com o Postgres fora.
- O dashboard versionado faz parte dos recursos criados por script (E02).
- Sem logs centralizados, investigar um Vídeo exige ler os logs de vários containers. O `correlation_id` torna isso viável no escopo.
- Fecha Q08 (logs estruturados e com correlação).
