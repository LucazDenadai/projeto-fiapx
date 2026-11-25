# Diagramas da arquitetura

Vocabulário do [glossário](../../GLOSSARY.md); as decisões estão nos [ADRs](../adr/).

## Componentes

Todos os containers do [`docker-compose.yml`](../../docker-compose.yml) e quem fala com quem. A seta sai de quem inicia a conexão. Só três portas saem da rede do Compose: a da API e, apenas em localhost, a do Grafana e a do Mailpit. Worker e Notifier não têm porta pública ([ADR-0001](../adr/0001-api-worker-notifier.md)).

```mermaid
flowchart TB
  usuario(["Usuário<br/>(Postman)"])
  operador(["Operador<br/>(localhost)"])

  subgraph compose["Rede interna do Compose"]
    api["API<br/>cadastro, login, envio,<br/>listagem, download<br/>+ relay da outbox"]
    worker["Worker × N<br/>ffmpeg, Zip de frames<br/>+ consumidor da DLQ, relay da outbox, limpador<br/>sem porta pública"]
    notifier["Notifier<br/>e-mail de Falha definitiva<br/>sem porta pública"]
    migrate["migrate<br/>execução única"]

    postgres[("Postgres<br/>Usuários, Vídeos, outbox")]
    redis[("Redis<br/>limite de tentativas")]
    rabbitmq[["RabbitMQ<br/>filas quorum e eventos"]]
    volume[("volume dados<br/>originais e zips")]

    mailpit["Mailpit<br/>SMTP e web"]
    prometheus["Prometheus"]
    grafana["Grafana"]
  end

  usuario -->|"HTTP :8080 (JWT)"| api
  operador -->|"HTTP :3000 (login)"| grafana
  operador -->|"HTTP :8025"| mailpit

  api -->|SQL| postgres
  api -->|RESP| redis
  api -->|"AMQP (relay)"| rabbitmq
  api -->|"grava original, lê zip"| volume

  worker -->|SQL| postgres
  worker -->|"AMQP (consome, relay)"| rabbitmq
  worker -->|"lê original, grava zip, limpa"| volume

  notifier -->|"AMQP (consome)"| rabbitmq
  notifier -->|"SMTP :1025"| mailpit
  migrate -->|"SQL, antes da API e do Worker"| postgres

  grafana -->|PromQL| prometheus
  prometheus -.->|"/metrics :9090"| api
  prometheus -.->|"/metrics :9090"| worker
  prometheus -.->|"/metrics :9090"| notifier
  prometheus -.->|"métricas :15692"| rabbitmq
```

API e Worker dividem o Postgres e o volume; o Notifier não acessa nenhum dos dois. A porta interna `9090` de cada processo serve `/metrics`, `/healthz` e `/readyz` ([ADR-0010](../adr/0010-observabilidade-prometheus-grafana.md)).

## Fluxo de um Vídeo

Do envio até o fim, em `concluido` ou `falhou`. A mensagem só carrega `video_id` e `correlation_id`; os bytes ficam no volume ([ADR-0002](../adr/0002-rabbitmq-mensageria.md), [ADR-0003](../adr/0003-transactional-outbox.md), [ADR-0004](../adr/0004-retry-e-dlq.md), [ADR-0008](../adr/0008-notificacao-por-email.md)).

```mermaid
flowchart TB
  envio(["Usuário envia o Vídeo<br/>POST /videos"])
  grava["API grava o original no volume"]
  txapi["Transação da API<br/>Vídeo <b>recebido</b> + linha na outbox"]
  aceito(["202 Accepted"])
  envio --> grava --> txapi --> aceito

  relay["Relay da outbox (API e Worker)<br/>publica com confirm e só então<br/>marca a linha como publicada"]
  txapi -->|"linha pendente"| relay
  relay --> fila[["fila processamento"]]

  subgraph worker["Worker: Processamento"]
    inicia["Marca <b>processando</b>"]
    extrai["ffprobe e ffmpeg:<br/>1 frame por segundo"]
    zip["Publica o Zip de frames no volume"]
    txok["Transação: <b>concluido</b> + evento na outbox"]
    ack["Apaga o original e dá ack"]
    inicia --> extrai --> zip --> txok --> ack
  end

  fila --> inicia
  ack --> download(["Usuário baixa o zip<br/>GET /videos/{id}/zip"])
  txok -->|"evento concluido, pelo relay"| eventos[["exchange processamento.eventos"]]

  extrai -->|"erro permanente: vídeo inválido,<br/>muito longo, sem frames, tempo excedido"| txfalha["Transação do Worker<br/><b>falhou</b> + Motivo + evento na outbox"]
  worker -.->|"erro transitório: backoff até ~1 min,<br/>depois nack; ou queda do Worker"| fila
  fila -->|"esgotou o delivery-limit<br/>(padrão 5)"| dlq[["processamento.dlq"]]
  dlq -->|"consumidor no Worker,<br/>tenta até conseguir: erro_interno"| txfalha

  txfalha -->|"evento falhou, pelo relay"| eventos
  eventos -->|"routing key falhou"| filanot[["fila notificacao"]]
  filanot --> notifier["Notifier"]
  notifier -->|SMTP| email(["E-mail ao Usuário<br/>com o Motivo da falha"])
  notifier -.->|"SMTP fora: backoff, depois nack"| filanot
  filanot -->|"esgotou o delivery-limit"| dlqnot[["notificacao.dlq<br/>parada para inspeção"]]
```

A entrega é pelo menos uma vez e o Worker é idempotente: se o Vídeo já é terminal, ele só dá ack. O evento `concluido` ainda não tem assinante.

## Estados do Vídeo

As transições do [ADR-0014](../adr/0014-ciclo-de-vida-do-video.md). Só andam para a frente: cada uma é um `UPDATE … WHERE status IN (<origens permitidas>)`.

```mermaid
stateDiagram-v2
  direction LR
  [*] --> recebido: API, na transação do envio
  recebido --> processando: Worker começa
  processando --> processando: reentrega após queda, recomeça do zero
  processando --> concluido: Worker, zip publicado
  recebido --> falhou: consumidor da DLQ, tentativas esgotadas
  processando --> falhou: erro permanente ou tentativas esgotadas
  concluido --> [*]
  falhou --> [*]
```

`recebido` e `processando` contam na Cota. `concluido` e `falhou` são finais: não há cancelamento, nem novo Processamento do mesmo Vídeo. A expiração do Zip de frames não muda o status ([ADR-0015](../adr/0015-retencao-e-limpeza.md)).
