---
status: accepted
resolve: RF02, RF05
---

# ADR-0004 — Política de retry e DLQ do Processamento e da notificação

## Contexto

O [ADR-0002](0002-rabbitmq-mensageria.md) definiu a garantia de RF02 (todo Vídeo aceito termina com Zip de frames ou com Falha definitiva notificada) e deixou em aberto os números do `delivery-limit`, a classificação de erros e o destino da DLQ. O [ADR-0003](0003-transactional-outbox.md) definiu que todo evento sai pela outbox. RF05 pede notificar o Usuário em caso de erro; o projeto base devolve a saída bruta do ffmpeg ao cliente (`legacy/main.go:146`).

Fatos do RabbitMQ que restringem a solução: quorum queue não tem atraso nativo no requeue; o `delivery-limit` conta toda devolução da mensagem, inclusive queda do consumidor com a mensagem em mãos; ao exceder o limite, o broker manda a mensagem à dead-letter exchange e não toca no Postgres.

## Decisão

**Classificação de erros do Processamento.**

- **Permanentes** (sem nova tentativa): arquivo não é vídeo ou não tem stream de vídeo; vídeo corrompido (ffmpeg sai com erro de decodificação); zero frames extraídos; timeout do ffmpeg.
- **Transitórios**: Postgres, storage ou RabbitMQ inacessíveis; erros de rede ou I/O de infraestrutura.
- **Todo erro não classificado conta como transitório**: ganha nova chance e, se persistir, chega à DLQ; a Falha definitiva fica garantida mesmo assim.

**Retry em duas camadas** (fila `processamento`):

1. Dentro do Worker, cada operação de infraestrutura tenta de novo com backoff exponencial (1, 2, 4, 8, 16 s…) até um orçamento de ~1 min.
2. Esgotado o orçamento, `nack` com requeue. O `delivery-limit` nativo da quorum queue (padrão **5**) conta a tentativa. Um Vídeo que derruba o Worker em loop (OOM, kill) também chega à DLQ, pois cada queda conta.

Na prática, tolera ~5 min de queda de infraestrutura antes da Falha definitiva.

**Falha definitiva: sempre o Worker a registra**, numa transação que marca o Vídeo `falhou` com o Motivo da falha e grava o evento `falhou` na outbox (ADR-0003).

- Erro permanente: o Worker faz isso direto e dá ack; a mensagem não passa pela DLQ.
- Tentativas esgotadas: a mensagem vai pela dead-letter exchange à fila `processamento.dlq`. Um consumidor dela, goroutine no Worker, faz a mesma transação e só então dá ack. Essa fila **não tem `delivery-limit`**: o consumidor tenta até conseguir, porque é ali que a garantia de RF02 se fecha.
- O Notifier não consome DLQ; ouve só o evento `falhou`. Um caminho até a notificação.

**Status terminal é final.** `concluido` e `falhou` são terminais; toda transição é `UPDATE … WHERE` status não terminal.

- O consumidor da DLQ que encontra o Vídeo já terminal só dá ack, sem evento (evita notificar falha de um Vídeo concluído por outra cópia da mensagem).
- O Worker que recebe um Vídeo já terminal dá ack e não reprocessa. Isto esclarece o ADR-0002: "reprocessar sobrescreve o zip" vale só para reentrega de Vídeo ainda não terminal (ex.: Worker caiu no meio).

**Depois da DLQ.** A `processamento.dlq` é drenada pelo consumidor; não há ferramenta de reprocessamento a partir dela. Para tentar de novo, o Usuário envia o vídeo outra vez, como Vídeo novo. "Reenviar o mesmo Vídeo" fica para o ciclo de vida do Vídeo.

**Motivo da falha** visível ao Usuário (listagem e notificação): código público com texto fixo — `video_invalido`, `sem_frames`, `tempo_excedido`, `erro_interno` ("não conseguimos processar, tente novamente"). Saída do ffmpeg, stack e detalhes vão só ao log, com `correlation_id`.

**Notifier**, fila `notificacao`: mesmo padrão (backoff no processo, `nack` com requeue, `delivery-limit` 5). Esgotado, a mensagem vai para `notificacao.dlq`, que **fica parada para inspeção**, com log de erro e métrica. Não há notificação sobre falha de notificação; notificação duplicada é aceita. O status `falhou` segue visível na listagem (RF04) mesmo que a notificação não chegue.

Todos os números (limites, orçamento, backoff) são configuráveis por variável de ambiente, o que permite baixá-los no vídeo de entrega para mostrar a DLQ.

## Opções consideradas

- **`nack` com requeue imediato, sem backoff no processo**: rejeitado. Com o banco fora, esgota as tentativas em segundos e falha Vídeos por queda de infraestrutura, contra RF02.
- **Fila de espera com TTL e dead-letter de volta a `processamento`**: rejeitado. Dá atraso real, mas a mensagem sai da fila principal e a contagem passa a depender de header da aplicação, o que o ADR-0002 evitou ao escolher quorum queue.
- **Erro desconhecido como permanente**: rejeitado. Um bug ou falha transitória não prevista falharia o Vídeo na primeira ocorrência.
- **Notifier consumindo a DLQ direto**: rejeitado. O status do Vídeo não seria atualizado e haveria dois caminhos até a notificação.
- **DLQ de `processamento` parada para reprocessamento manual**: rejeitado. Exige ferramenta e operação fora do escopo do enunciado; o Usuário já é notificado e pode reenviar.
- **Repassar a mensagem do ffmpeg ao Usuário**: rejeitado. Vaza detalhe interno (problema do projeto base).

## Consequências

- O Worker roda três consumidores: `processamento`, `processamento.dlq` e o relay da outbox.
- O Vídeo ganha a coluna do Motivo da falha; o conjunto de códigos é contrato público (listagem e notificação).
- O timeout do ffmpeg (A9) somado ao orçamento de backoff tem de ficar abaixo do `consumer_timeout` do RabbitMQ (padrão 30 min); senão o broker fecha o canal e conta uma tentativa espúria.
- Métricas para A8: profundidade de `processamento.dlq` e `notificacao.dlq`, contagem de falhas por motivo.
- Topologia declarada na subida (filas, dead-letter exchanges, `delivery-limit`); testes de integração cobrem o caminho até a DLQ com limite baixo.
- Libera o ciclo de vida do Vídeo (estados, reenvio), que termina no `GLOSSARY.md`.
