---
status: accepted
resolve: RF05, A6
---

# ADR-0008 — Notificação de Falha definitiva por e-mail (SMTP, Mailpit no Compose)

## Contexto

RF05 pede que, em caso de erro, o Usuário possa ser notificado "por e-mail ou outro meio". No projeto base não há notificação: o erro bruto do ffmpeg vaza na resposta HTTP (Q06, `legacy/main.go:146`). Já decidido:

- o Notifier consome o evento `falhou` pela fila `notificacao` ([ADR-0001](0001-api-worker-notifier.md), [ADR-0002](0002-rabbitmq-mensageria.md));
- o evento sai pela outbox ([ADR-0003](0003-transactional-outbox.md));
- retry, `notificacao.dlq` e a aceitação de notificação duplicada estão no [ADR-0004](0004-retry-e-dlq.md), junto com os códigos públicos do Motivo da falha;
- o login é por e-mail, sem verificação ([ADR-0007](0007-autenticacao-jwt-na-api.md)).

## Decisão

**O Notifier envia um e-mail por SMTP a cada Falha definitiva. No Compose, o servidor SMTP é o Mailpit.**

- **Meio: e-mail por SMTP.** Host, porta, remetente e credenciais opcionais vêm de variáveis de ambiente. No Compose, o serviço `mailpit` recebe o SMTP e mostra os e-mails numa interface web, que é a evidência no vídeo de entrega. Usar um SMTP real é só mudar a configuração. O envio usa `context.Context` com timeout. Falha de SMTP é erro transitório e segue o ADR-0004.
- **Só Falha definitiva.** O evento `concluido` não gera notificação, porque o RF05 pede aviso só em caso de erro. O status `concluido` aparece na listagem (RF04).
- **O evento leva os dados do e-mail:** `falhou` = `{video_id, correlation_id, email, nome_original, motivo, ocorrido_em}`. O Worker preenche esses dados na mesma transação que grava `falhou` e a linha da outbox (ADR-0003). O Notifier não acessa o Postgres: não tem estado nem credencial de banco, e o banco continua compartilhado só entre a API e o Worker (ADR-0001).
- **Conteúdo: somente texto** (`text/plain`, UTF-8), em português, sem HTML e sem links.
  - Assunto: "Não conseguimos processar seu vídeo".
  - Corpo: o nome original sanitizado (ADR-0006), a data, o texto fixo do Motivo da falha e a sugestão de enviar o vídeo de novo.
  - Sem saída do ffmpeg, stack ou ids técnicos além do `video_id`. O detalhe fica no log, com o `correlation_id`.
  - Os campos vindos do evento não podem ter quebra de linha nos cabeçalhos, o que evita injeção de cabeçalho SMTP.

## Opções consideradas

- **Webhook**: rejeitado. O Usuário teria de cadastrar uma URL, e o sistema precisaria se proteger contra SSRF e lidar com endpoints lentos, sem requisito que peça isso.
- **Só o status dentro do sistema**: rejeitado. A listagem já mostra `falhou`, mas isso não é notificar. O RF05 pede um aviso que chegue ao Usuário.
- **Notificar também o sucesso**: rejeitado. Fora do RF05; dobraria os e-mails e os templates. Pode ser somado depois assinando `concluido`, sem mudar o Worker (ADR-0002).
- **Evento só com `video_id`, Notifier consultando o Postgres**: rejeitado. Daria acesso ao banco a um terceiro processo. A única vantagem, ler o e-mail atualizado, não existe: não há troca de e-mail (ADR-0007).
- **E-mail em HTML**: rejeitado. O nome do arquivo vem do Usuário; HTML exigiria escape cuidadoso para não injetar marcação, e não traz ganho para um aviso curto.
- **Provedor de e-mail por API (SES, SendGrid)**: rejeitado. Exige conta externa e credencial, e não roda só no Compose. O SMTP atende a qualquer provedor depois.

## Consequências

- O Compose ganha o serviço `mailpit`. A interface web dele fica exposta só em desenvolvimento, para o vídeo.
- O evento `falhou` vira contrato entre o Worker e o Notifier, e carrega dado pessoal (e-mail) na fila. Isso é aceito, porque a fila só existe na rede interna do Compose.
- O e-mail vai para o endereço informado no cadastro, que não é verificado (ADR-0007).
- Notificação duplicada é possível na reentrega (ADR-0004). Um e-mail que não chega não muda o status `falhou`, que continua visível na listagem.
- Os testes do Notifier usam um servidor SMTP falso em memória. O teste de integração do Compose confere o e-mail no Mailpit pela API HTTP dele.
- Fecha Q06: nenhuma saída do ffmpeg chega ao Usuário, nem na resposta nem no e-mail.
