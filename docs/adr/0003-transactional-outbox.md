---
status: accepted
resolve: RF02, A3
---

# ADR-0003 — Transactional Outbox para publicar no RabbitMQ

## Contexto

O [ADR-0002](0002-rabbitmq-mensageria.md) definiu a garantia de RF02: depois que a API aceita o Vídeo, o Processamento termina com Zip de frames pronto ou com falha definitiva notificada. Mas há duas escritas em sistemas diferentes, sem transação comum:

- **API:** grava o Vídeo no Postgres e publica o Job `{video_id, correlation_id}` na fila `processamento`. Se cair entre os dois passos, o Vídeo fica aceito e nunca é processado.
- **Worker:** grava o status `falhou`/`concluido` no Postgres e publica o evento em `processamento.eventos`. Se cair entre os dois, o Usuário nunca é notificado.

## Decisão

**Transactional Outbox**, uma tabela `outbox` genérica no Postgres usada pela API e pelo Worker.

- **Escrita:** na mesma transação da mudança de estado, o processo insere uma linha na `outbox` com exchange, routing key e payload. Nada é publicado direto no RabbitMQ depois de uma escrita no Postgres.
- **Ordem do upload na API:** (1) grava os bytes no storage (A4); (2) transação com Vídeo (status inicial) + linha da outbox; (3) responde `202 Accepted`. Queda entre 1 e 2 deixa só um arquivo órfão no storage, sem Vídeo aceito; a limpeza é do ticket de retenção.
- **Relay:** goroutine dentro da API e do Worker (sem processo novo; mantém o [ADR-0001](0001-api-worker-notifier.md)). Faz polling (intervalo configurável, padrão 1 s) de um lote com `SELECT … WHERE publicado_em IS NULL … FOR UPDATE SKIP LOCKED LIMIT n`, de modo que réplicas não disputem a mesma linha.
- **Publicação:** com `mandatory` e **publisher confirm**. Só após o confirm, na mesma transação do lock, grava `publicado_em = now()`. A linha permanece como histórico; um índice parcial `WHERE publicado_em IS NULL` mantém o polling rápido. Expurgo das linhas publicadas: ticket de retenção.
- **Falha ao publicar** (broker fora, nack ou mensagem sem rota): a linha fica pendente e o relay tenta de novo **sem limite de tentativas**, com backoff exponencial limitado a ~30 s. Descartar quebraria a garantia de RF02.
- **Duplicatas são aceitas:** queda entre o confirm e o `publicado_em` republica a linha. A entrega já é pelo menos uma vez e o Worker é idempotente (ADR-0002).
- **Sem garantia de ordem** entre linhas da outbox; nenhum consumidor pode depender dela. Cada Vídeo é independente e `falhou`/`concluido` são terminais.
- **Job sem Vídeo** não ocorre, pois a publicação só acontece após o commit. Se o Worker receber `video_id` inexistente, dá ack, descarta e registra log de erro.

## Opções consideradas

- **Publicar logo após o commit**: rejeitado. Mantém a janela de perda e falha sempre que o RabbitMQ estiver fora, exatamente no cenário de pico/instabilidade de RF02.
- **Publicar após o commit + reconciliador de órfãos**: rejeitado. Precisa de um timeout para definir "Vídeo parado", que confunde "nunca publicado" com "Worker lento", e reenfileira trabalho em andamento.
- **Fila no Postgres** (eliminaria a escrita dupla): já rejeitada no ADR-0002.
- **Relay como quarto processo**: rejeitado. Contraria o ADR-0001 sem ganho; com `SKIP LOCKED` o relay escala junto com as réplicas da API e do Worker.
- **`LISTEN/NOTIFY` ou publicação imediata com polling de reserva**: rejeitados. Ganham menos de 1 s de latência num Processamento que leva de segundos a minutos, e trazem casos de corrida.
- **Apagar a linha após o confirm**: rejeitado. Some o histórico de "quando o Job saiu"; com a observabilidade (A8) ainda aberta, a tabela é a única fonte garantida. Medir atraso (`publicado_em - criado_em`) em SQL também ajuda na depuração e no vídeo de entrega.

## Consequências

- Todo processo que publica mensagem tem relay e acesso à tabela `outbox`. Migrations (E02) criam a tabela e o índice parcial.
- Com o RabbitMQ fora do ar, a API continua aceitando Vídeos; a outbox acumula e esvazia quando o broker volta. Essa cena entra no roteiro do vídeo de entrega (E04).
- Latência entre o aceite e o Job na fila de até ~1 intervalo de polling.
- Métricas para A8: linhas pendentes na outbox e idade da mais antiga.
- O ticket de retenção passa a cobrir também os arquivos órfãos no storage e o expurgo da outbox.
- Libera a [Política de retry e DLQ](https://github.com/LucazDenadai/projeto-fiapx/issues/14) e o ciclo de vida do Vídeo.
