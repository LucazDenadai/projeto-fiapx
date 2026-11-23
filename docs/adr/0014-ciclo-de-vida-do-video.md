---
status: accepted
resolve: RF04, RF02
---

# ADR-0014 — Ciclo de vida do Vídeo: quatro estados, transições só para a frente

## Contexto

RF04 pede a listagem de status dos Vídeos de um Usuário. No projeto base não existe status, só zips prontos no disco (`legacy/main.go:254`). Já decidido:

- a API cria o Vídeo e só o Worker avança o status ([ADR-0001](0001-api-worker-notifier.md));
- a mensagem só recebe ack depois do zip, e a reentrega após uma queda refaz o Processamento ([ADR-0002](0002-rabbitmq-mensageria.md));
- o Vídeo e a linha da outbox são gravados na mesma transação ([ADR-0003](0003-transactional-outbox.md));
- `concluido` e `falhou` são terminais, e toda transição é `UPDATE … WHERE` status não terminal. "Reenviar o mesmo Vídeo" ficou em aberto ([ADR-0004](0004-retry-e-dlq.md));
- a cota conta Vídeos não terminais ([ADR-0011](0011-escala-e-limites.md)).

## Decisão

**Quatro estados. As transições só andam para a frente. Quem faz cada uma é fixo.**

- **Estados:**
  - `recebido`: aceito (`202`), aguardando um Worker;
  - `processando`: um Worker está extraindo os frames;
  - `concluido`: o Zip de frames está pronto para download;
  - `falhou`: Falha definitiva, com o Motivo da falha.

  `recebido` e `processando` são os não terminais que contam na cota.
- **Transições:**

  | De → para | Quem | Quando |
  |---|---|---|
  | (nada) → `recebido` | API | Na transação do envio, junto com a outbox (ADR-0003) |
  | `recebido` → `processando` | Worker | Ao começar o Processamento |
  | `processando` → `processando` | Worker | Reentrega depois de uma queda: aceita, e o Processamento recomeça do zero |
  | `processando` → `concluido` | Worker | Depois de publicar o zip no volume, na transação com o evento na outbox |
  | `recebido` ou `processando` → `falhou` | Worker ou consumidor da `processamento.dlq` | Erro permanente ou tentativas esgotadas (ADR-0004) |

  `recebido` → `falhou` direto existe porque o Worker pode cair antes de marcar `processando` e, mesmo assim, a mensagem esgota as tentativas. Toda transição é um `UPDATE … WHERE status IN (<origens permitidas>)`. Um `UPDATE` que não afeta linha nenhuma significa que o Vídeo já é terminal: o Worker dá ack e segue (ADR-0004). A API nunca muda o status depois de criar o Vídeo.
- **Sem vigia de Vídeo parado:** nada reverte `processando` por tempo. Se o Worker morre, a mensagem sem ack volta para a fila, outro Worker a pega, e o status continua `processando` até terminar. A garantia vem do broker (ADR-0002).
- **Listagem** (`GET /videos`, `GET /videos/{id}`):
  - campos: `id`, `nome_original`, `status`, `criado_em`, `atualizado_em`;
  - `motivo` e o texto fixo dele, só quando o status é `falhou`;
  - `download` (`/videos/{id}/zip`), só quando o status é `concluido`.

  Ordem do mais recente para o mais antigo. Paginação por cursor (`criado_em`, `id`), com `?limite=` (padrão 20, máximo 100). Número de tentativas e Worker responsável ficam fora, porque são internos. Só lista Vídeos do Usuário do token; Vídeo de outro Usuário dá `404` (ADR-0006).
- **Sem reenviar o mesmo Vídeo:** para tentar de novo, o Usuário envia o arquivo outra vez, o que cria um Vídeo novo. Não há rota de reprocessar. Isso fecha a questão deixada em aberto pelo ADR-0004.
- **Sem apagar nem cancelar:** não há `DELETE /videos/{id}` nem cancelamento de Processamento. A limpeza é da retenção (ticket próprio).

## Opções consideradas

- **Estado `na_fila` separado de `recebido`**: rejeitado. Depois do `202`, o Vídeo já está garantido (outbox ou fila). Distinguir "na outbox" de "na fila" é detalhe do relay, sem significado para o Usuário.
- **Vigia que reverte `processando` parado**: rejeitado. Duplicaria a reentrega que o RabbitMQ já faz e poderia rodar dois Processamentos do mesmo Vídeo ao mesmo tempo.
- **Reprocessar o mesmo Vídeo (`POST /videos/{id}/reprocessar`)**: rejeitado. Contradiria o status terminal do ADR-0004 e obrigaria a guardar o original dos Vídeos que falharam.
- **Apagar ou cancelar pelo Usuário**: rejeitado. Sem requisito. Cancelar exigiria sinalizar o Worker que está rodando o ffmpeg.
- **Paginação por `offset`**: rejeitado. Com Vídeos novos entrando, as páginas se deslocam e repetem itens. O cursor é estável.
- **Expor o número de tentativas na listagem**: rejeitado. É detalhe interno e confundiria o Usuário ("tentativa 3 de 5").

## Consequências

- A tabela de Vídeos tem `status` com `CHECK` nos quatro valores e índice em (`usuario_id`, `criado_em`, `id`) para a listagem e para a cota.
- Um Vídeo pode ficar em `processando` durante várias reentregas. O Usuário não vê as tentativas, só o fim.
- Os testes cobrem cada transição permitida e a recusa das proibidas (ex.: `concluido` → `processando` não muda nada).
- Os estados entram no `GLOSSARY.md`.
