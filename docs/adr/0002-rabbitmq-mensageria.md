---
status: accepted
resolve: RF01, RF02, S02, A2
---

# ADR-0002 — RabbitMQ com quorum queues para Processamento e eventos

## Contexto

O [ADR-0001](0001-api-worker-notifier.md) separou API, Worker e Notifier. Faltam os canais API → Worker (pedir o Processamento de um Vídeo) e Worker → Notifier (avisar falha definitiva). RF01 pede vários Vídeos em paralelo; RF02 pede não perder requisição em picos. No projeto base não há fila: o Processamento roda no request e some se o processo cair.

## Decisão

**Garantia (definição de RF02 neste projeto):** depois que a API responde que aceitou o Vídeo, o Processamento dele **termina** — com Zip de frames pronto ou com falha definitiva notificada ao Usuário — mesmo que qualquer container caia no caminho. Entrega **pelo menos uma vez**; o Worker é **idempotente** (reprocessar o mesmo Vídeo sobrescreve o zip e não regride o status).

**Mecanismo:** RabbitMQ, um broker para os dois fluxos.

- Fila `processamento` (quorum, durável, mensagens persistentes): a API publica; réplicas do Worker competem por ela. O Worker dá **ack só depois de salvar o Zip de frames**; se morrer antes, o broker reentrega a outro Worker.
- `delivery-limit` da quorum queue + dead-letter exchange levam à DLQ a mensagem que excede as tentativas. Números e classificação de erros: ticket "Política de retry e DLQ".
- Exchange topic `processamento.eventos`: o Worker publica `falhou` (e `concluido`); o Notifier consome pela fila própria `notificacao`, ligada a `falhou`. Novos consumidores assinam sem mudar o Worker.
- Mensagem carrega só `video_id` e `correlation_id`; o vídeo nunca trafega pela fila, o resto o Worker lê do Postgres e do storage.

## Opções consideradas

- **Kafka**: rejeitado. Paralelismo preso ao nº de partições, sem ack por mensagem nem DLQ nativa; replay não tem uso aqui; mais pesado no Compose.
- **Fila no Postgres (`FOR UPDATE SKIP LOCKED`)**: rejeitado, apesar de eliminar a escrita dupla (A3). Polling, lease de Worker morto, tentativas, DLQ e fan-out para o Notifier teriam de ser escritos à mão; e desvia da stack recomendada (S02) sem necessidade.
- **Classic queues**: rejeitado. Exigiriam contar tentativas na aplicação; quorum traz `delivery-limit` nativo e é o tipo recomendado nas versões atuais.
- **Payload completo na mensagem**: rejeitado. Duplica estado que já está no Postgres e pode ficar velho.
- **Exactly-once**: descartado por inexistir entre sistemas distintos; idempotência no Worker cumpre o mesmo papel.

## Consequências

- Gravar o Vídeo (Postgres) e publicar em `processamento` (RabbitMQ) são escritas em sistemas diferentes: A3 precisa fechar essa janela.
- O Worker tem de ser idempotente e o ack é o último passo do Processamento.
- `prefetch` por Worker é o controle de concorrência local; `--scale worker=N` é o horizontal (A9).
- RabbitMQ entra no Compose com o plugin de management (profundidade de fila visível no vídeo de entrega, E04) e é fonte de métricas para A8.
- Cliente Go: `github.com/rabbitmq/amqp091-go` (cliente oficial).
