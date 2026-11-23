---
status: accepted
resolve: RT02, RF01, A9
---

# ADR-0011 — Escala pelo Worker e limites que protegem CPU, memória e disco

## Contexto

O projeto base processa dentro da requisição, sem limite de tamanho, de duração nem de tempo (Q05, Q07). Os ADRs anteriores deixaram números em aberto para o A9:

- a concorrência por Worker (`prefetch`, [ADR-0002](0002-rabbitmq-mensageria.md));
- o timeout do ffmpeg, que precisa caber no `consumer_timeout` ([ADR-0004](0004-retry-e-dlq.md));
- o disco do Worker ([ADR-0005](0005-volume-arquivos.md));
- o teto e o timeout do envio ([ADR-0006](0006-envio-e-download-pela-api.md)).

A extração continua a do projeto base: 1 frame por segundo, em PNG.

## Decisão

**A escala demonstrada é a do Worker (réplicas competindo na fila). Limites em cada camada impedem que um Vídeo ou um Usuário esgote os recursos.** Todos os valores abaixo são padrões configuráveis por variável de ambiente.

- **Concorrência:** `prefetch` 1 por Worker, ou seja, um ffmpeg por container. O Compose sobe **2 réplicas de Worker** por padrão, então RF01 vale sem comando extra. Para escalar: `docker compose up --scale worker=N`.
- **API: 1 réplica.** Ela não guarda estado (volume compartilhado, JWT, Redis), então é escalável horizontalmente. Várias réplicas exigiriam um proxy reverso na frente da porta publicada. Fica como evolução.
- **Envio:** teto de **500 MB** por Vídeo (`413`) e timeout de leitura de **10 min** na rota de envio (ADR-0006).
- **Cota por Usuário:** no máximo **5 Vídeos não terminais** por Usuário; o sexto envio recebe `429`. A API confere antes de gravar o arquivo, para recusar barato. Também confere de novo dentro da transação do ADR-0003, com lock na linha do Usuário (`SELECT … FOR UPDATE`), para que dois envios simultâneos não passem os dois. Se a segunda checagem recusar, o arquivo gravado é apagado.
- **Duração máxima: 10 min** (até 600 frames). O Worker confere com o `ffprobe` antes de extrair. Acima disso, Falha definitiva com o Motivo novo **`video_muito_longo`**, que se soma aos códigos do ADR-0004.
- **Timeout do ffmpeg: 10 min** por Processamento, com `exec.CommandContext` (fecha Q07). Estourou, Falha definitiva com `tempo_excedido` (ADR-0004). Os 10 min, somados ao orçamento de backoff de ~1 min, ficam abaixo dos 30 min do `consumer_timeout` do RabbitMQ.
- **CPU e memória por container** (`deploy.resources.limits`):

  | Container | CPU | Memória |
  |---|---|---|
  | Worker | 1 | 1 GB |
  | API | 0,5 | 256 MB |
  | Notifier | 0,25 | 128 MB |

  Um Vídeo que estoura a memória derruba só o seu Worker. Cada queda conta uma tentativa no `delivery-limit` e, esgotadas as tentativas, ele vai para a DLQ (ADR-0004).

## Opções consideradas

- **Vários Processamentos por Worker (`prefetch` > 1)**: rejeitado como padrão, mas continua configurável. Vários ffmpeg no mesmo container disputam o mesmo limite de CPU, e a escala fica menos visível no vídeo. Uma réplica por unidade de concorrência deixa limite e escala na mesma unidade.
- **Uma réplica de Worker por padrão**: rejeitado. RF01 só valeria depois de um `--scale`.
- **Várias réplicas da API com proxy reverso (nginx)**: rejeitado no escopo. É mais um container, e o gargalo é o ffmpeg, não a API.
- **Conferir a duração na API**: rejeitado. Poria o ffmpeg na imagem da API (rejeitado no ADR-0006).
- **Reusar `video_invalido` para vídeo longo demais**: rejeitado. O Usuário não saberia que o problema é a duração.
- **Sem cota por Usuário**: rejeitado. Um único Usuário encheria o disco e a fila; o limite de tentativas (ADR-0009) cobre só login e cadastro.
- **Checar a cota só antes de gravar**: rejeitado. Dois envios simultâneos passariam os dois.
- **Autoscaling de Workers pelo tamanho da fila (KEDA ou script)**: rejeitado. Kubernetes está fora de escopo, e um script de autoscaling no Compose é infraestrutura caseira sem requisito que peça.

## Consequências

- **Disco efêmero do Worker:** no pior caso, 600 frames PNG em 1080p (~2–3 MB cada), mais o zip do mesmo tamanho, que PNG quase não comprime. São ~3–4 GB por Processamento em andamento. Com `prefetch` 1, isso vale por container.
- **Volume:** comporta no máximo 5 Vídeos em andamento por Usuário, de até 500 MB cada, além dos zips. A retenção (ticket próprio) é o que impede o volume de crescer sem fim.
- O Motivo da falha ganha `video_muito_longo`. O contrato público (listagem e notificação) e o `GLOSSARY.md` são atualizados.
- No vídeo de entrega, os limites podem ser baixados por variável de ambiente (ex.: 30 s de duração) para mostrar `video_muito_longo` e `tempo_excedido` sem esperar 10 min.
- Os testes cobrem a cota com envios concorrentes, o `ffprobe` acima da duração e o timeout do ffmpeg (com timeout de teste curto).
- Fecha Q05 (limites de tamanho, duração e quantidade) e Q07 (timeout com `context`).
