# Roteiro do vídeo de entrega (E04)

Até 10 minutos, com documentação, arquitetura e o projeto funcionando ([enunciado](enunciado.md)). O roteiro soma 9min30s. Os comandos e os tempos da parte ao vivo foram medidos na stack publicada.

Em cada bloco: **Tela** é o que mostrar ou digitar; **Fala** é um texto de apoio, para dizer com as suas palavras.

## Antes de gravar

1. Pare a stack que estiver no ar. Nada é apagado:

   ```sh
   docker compose stop
   ```

2. No `.env`, só para a gravação:

   ```sh
   COMPOSE_PROJECT_NAME=gravacao   # stack separada, com volumes novos
   TAG=1.0.1
   LIMITE_CADASTRO_IP_MAX=50       # o padrão é 5 por hora, e o pico usa 8
   ```

3. Suba a stack e prepare os arquivos (Git Bash, na raiz do repositório):

   ```sh
   docker compose pull
   docker compose up -d --no-build --wait
   docker pull alpine
   ffmpeg -f lavfi -i testsrc=duration=10:size=640x360:rate=25 -pix_fmt yuv420p bom.mp4
   head -c 20000 bom.mp4 > ruim.mp4
   go build -o pico.exe ./cmd/pico
   ./pico.exe          # aquece a máquina e enche o dashboard; espere terminar
   ```

4. Deixe abertos, nesta ordem:
   - navegador: README, matriz, ADR-0003, `docs/diagramas/`, Actions, pacotes do GHCR;
   - Grafana (http://localhost:3000), já logado, últimos 5 minutos, atualização de 5 s;
   - Mailpit (http://localhost:8025);
   - Postman com a coleção e o ambiente de [`docs/api/`](api/), e o `bom.mp4` escolhido no **Enviar vídeo**;
   - dois terminais Git Bash na raiz do repositório, com letra grande.
5. Feche o `.env`: nenhum segredo na tela. Silencie as notificações.

Depois de gravar: `docker compose down -v` (apaga só a stack `gravacao`), desfaça o passo 2 no `.env` e rode `docker compose start`.

## Roteiro

### 0:00 — Abertura (30 s)

**Tela:** README no GitHub.

**Fala:** "Sou [nome], [RM], e este é o meu projeto da fase 5. O ponto de partida era um protótipo que recebe um vídeo, extrai os frames e devolve um zip. Ele processa dentro da requisição, não tem usuário e, se cair, perde o pedido. Entreguei três processos em Go, com RabbitMQ, Postgres, Redis e observabilidade, tudo em Docker Compose."

### 0:30 — Documentação (1min30s)

**Tela:** índice do README → [matriz](matriz-requisitos.md), seções 1 e 7 → [ADR-0003](adr/0003-transactional-outbox.md), seção "Opções consideradas" → pasta `migrations/`.

**Fala:** "O README é o índice de tudo. A matriz pega cada requisito do enunciado, mostra o problema no projeto base e aponta a decisão e a evidência, no fim com a conferência final. As decisões estão em dezesseis ADRs. Este, por exemplo, explica por que usei o Transactional Outbox, o que rejeitei e o que isso custa. O banco é criado por migrations versionadas, que um serviço próprio aplica antes de a API subir."

### 2:00 — Arquitetura (1min45s)

**Tela:** [componentes](diagramas/README.md#componentes) → [fluxo de um Vídeo](diagramas/README.md#fluxo-de-um-vídeo) → [estados](diagramas/README.md#estados-do-vídeo).

**Fala (componentes):** "A API é a única porta de entrada do usuário. O Worker processa os vídeos e escala com réplicas. O Notifier só avisa quando algo falha. Worker e Notifier não têm porta pública."

**Fala (fluxo):** "Três pontos garantem que nenhum pedido se perde. Primeiro: o vídeo e a mensagem são gravados na mesma transação, na outbox; aceito é aceito. Segundo: o Worker só confirma a mensagem com o zip pronto, e pode repetir sem estragar nada. Terceiro: se as tentativas acabam, a mensagem vai para a DLQ, o vídeo termina como falho e o usuário recebe um e-mail."

**Fala (estados):** "O status só anda para a frente: recebido, processando, e depois concluído ou falhou."

### 3:45 — Ao vivo: fluxo feliz (1min15s)

**Tela:**
- terminal 1: `docker compose ps`
- Postman: **Listar vídeos** (`401`) → **Cadastro** → **Login** → **Enviar vídeo** → **Listar vídeos** → **Aguardar conclusão** → **Baixar zip**; abra o zip.

**Fala:** "A stack inteira está no ar, com as imagens publicadas e dois Workers. Sem token, a API responde 401. Faço o cadastro e o login, e o token fica salvo na coleção. Envio um vídeo: a resposta 202 vem na hora, sem esperar o processamento. Na listagem vejo só os meus vídeos e o status. Concluído, baixo o zip: um frame por segundo."

### 5:00 — Ao vivo: falha e notificação (45 s)

**Tela:** Postman, **Enviar vídeo** com `ruim.mp4` → **Listar vídeos** → Mailpit, abrir o e-mail.

**Fala:** "Agora um arquivo corrompido. O começo parece um vídeo, então ele é aceito. O Worker descobre o problema, e o vídeo termina em falhou, com um motivo que o usuário entende. A saída do ffmpeg fica só no log. E o e-mail de aviso já chegou."

### 5:45 — Ao vivo: pico, queda e escala (2min15s)

**Tela:**

| Tempo | Onde | O que fazer |
|---|---|---|
| 5:45 | terminal 1 | `./pico.exe -n 40` |
| 5:55 | Grafana | a fila sobe para perto de 36 |
| 6:10 | terminal 2 | matar um Worker (comando abaixo) e `docker compose ps worker` |
| 6:30 | terminal 2 | `docker compose up -d --no-build --no-recreate --scale worker=4` |
| 6:40 | Grafana | a fila escoa |
| 7:35 | terminal 1 | 40 concluídos, 0 falhos |

```sh
docker run --rm --privileged --pid=host alpine kill -9 $(docker inspect -f '{{.State.Pid}}' $(docker compose ps -q worker | head -1))
```

Use este comando, e não `docker kill`, que conta como parada manual e não dispara o reinício.

**Fala:** "Simulo um pico: quarenta vídeos de uma vez. Todos são aceitos, e a fila absorve. Agora mato um Worker no meio do processamento, como numa queda de verdade. A mensagem dele volta para a fila, e o container sobe sozinho. Para dar mais vazão, passo de dois para quatro Workers, sem mudar nada no código. No Grafana dá para ver a fila escoando mais rápido. No fim, quarenta concluídos e nenhum perdido, mesmo com a queda."

### 8:00 — Ao vivo: RabbitMQ fora do ar (45 s)

**Tela:** terminal 2 `docker compose stop rabbitmq` → Postman **Enviar vídeo** com `bom.mp4` (`202`) → `docker compose start rabbitmq` → **Aguardar conclusão**.

**Fala:** "E se o próprio broker cair? Paro o RabbitMQ e envio um vídeo: continua aceito, porque o pedido está na outbox, no banco. Religo, o relay publica, e o vídeo conclui."

Leva cerca de 15 s depois do `start`.

### 8:45 — Qualidade e CI/CD (35 s)

**Tela:** Actions, uma execução do CD aberta → pacotes do GHCR.

**Fala:** "Todo PR passa por lint, testes com Postgres, RabbitMQ e Redis reais, e varredura de vulnerabilidades. No merge, o CD publica as imagens, sobe a stack inteira no runner, roda esta mesma coleção do Postman e ainda recria o RabbitMQ com um vídeo na fila. Só então a versão avança."

### 9:20 — Fechamento (10 s)

**Tela:** README.

**Fala:** "Todos os requisitos obrigatórios estão atendidos, e o repositório é público. Obrigado!"

## Se o tempo apertar

Corte nesta ordem: abrir o zip, a seção do RabbitMQ fora do ar, o ADR-0003. Não corte documentação, arquitetura nem o fluxo feliz: o enunciado pede os três.

## Se algo der errado

| Sintoma | Causa provável | Saída |
|---|---|---|
| `429` no cadastro | Limite por IP | `docker compose restart redis` zera os contadores |
| `429` no envio | Cota de 5 Vídeos em andamento | Espere terminar, ou refaça Cadastro e Login |
| Fila não aparece no Grafana | Coleta a cada 15 s | Espere duas coletas |
| `401` nas rotas de Vídeo | Token de 1 h expirou | Rode o Login de novo |
| `kill` ou `$(...)` dá erro | Terminal PowerShell | Use o Git Bash |
