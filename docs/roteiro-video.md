# Roteiro do vídeo de entrega (E04)

Até 10 minutos, com documentação, arquitetura e o projeto funcionando ([enunciado](enunciado.md)). O roteiro soma 9min45s. A parte ao vivo foi ensaiada com as imagens publicadas.

## Antes de gravar

1. No `.env`, só para a gravação: `LIMITE_CADASTRO_IP_MAX=50`. O padrão é 5 cadastros por hora, e o pico usa 4.
2. Suba a stack do zero. **O `-v` apaga os dados locais.**

   ```sh
   docker compose down -v
   docker compose pull
   docker compose up -d --no-build --wait
   ```

3. Gere o vídeo corrompido e compile o pico, para não esperar o build na gravação:

   ```sh
   ffmpeg -f lavfi -i testsrc=duration=10:size=640x360:rate=25 -pix_fmt yuv420p bom.mp4
   head -c 20000 bom.mp4 > ruim.mp4
   go build -o pico.exe ./cmd/pico
   ```

4. Rode `./pico.exe` uma vez e espere terminar: aquece a máquina e enche o dashboard.
5. Deixe abertos:
   - GitHub: README, `docs/adr/`, `docs/diagramas/`, aba Actions e pacotes;
   - Postman com a coleção e o ambiente de `docs/api/`;
   - Grafana em http://localhost:3000, já logado, últimos 15 minutos, atualização de 5 s;
   - Mailpit em http://localhost:8025;
   - dois terminais Git Bash na raiz do repositório (os comandos não são do PowerShell).
6. Feche o `.env` e o gerenciador de senhas: nada com segredo na tela.

## Roteiro

### 0:00 — Abertura (30 s)

- Nome, curso e o problema: o protótipo processa dentro da requisição, sem usuário, sem fila e perde o pedido se cair.
- O que foi entregue: três processos em Go, com RabbitMQ, Postgres, Redis, Prometheus e Grafana, em Docker Compose.

### 0:30 — Documentação (1min30s)

- README: índice, como rodar, CI/CD.
- [Matriz de requisitos](matriz-requisitos.md): cada requisito do enunciado, o problema no projeto base e a evidência (seção 7).
- [ADRs](adr/): 16 decisões com opções rejeitadas e consequências. Abra um, o [ADR-0003](adr/0003-transactional-outbox.md).
- Script de criação do banco: `migrations/`, aplicado pelo serviço `migrate` (E02).

### 2:00 — Arquitetura (2 min)

- [Componentes](diagramas/README.md#componentes): API, Worker e Notifier; só a API tem porta pública de negócio.
- [Fluxo de um Vídeo](diagramas/README.md#fluxo-de-um-vídeo), com o dedo em três pontos:
  - o Vídeo e a mensagem são gravados na mesma transação (outbox), então aceito não se perde;
  - ack só depois do zip pronto, com o Worker idempotente;
  - DLQ: esgotadas as tentativas, o Vídeo termina em `falhou` e o Usuário recebe e-mail.
- [Estados](diagramas/README.md#estados-do-vídeo): só andam para a frente.

### 4:00 — Funcionando: fluxo feliz (1min15s)

- `docker compose ps`: tudo healthy, 2 Workers.
- Postman, em ordem: **Listar vídeos** sem token (`401`), Cadastro, Login (o token é salvo sozinho), Enviar vídeo (`202` na hora), Aguardar conclusão, Baixar zip. Abra o zip e mostre os frames.

### 5:15 — Funcionando: falha e notificação (45 s)

- No Postman, troque o arquivo do **Enviar vídeo** por `ruim.mp4`: ele tem cabeçalho de vídeo, então é aceito (`202`).
- **Listar vídeos**: `falhou`, com `video_invalido` e o texto público. Nada da saída do ffmpeg.
- Mailpit: o e-mail com o nome do arquivo, a data e o motivo.

### 6:00 — Funcionando: pico, queda e escala (2 min)

- Terminal 1: `./pico.exe` (20 envios em paralelo).
- Grafana: a fila sobe; 2 Processamentos em andamento.
- Terminal 2, um Worker morre no meio do Processamento:

  ```sh
  docker kill -s KILL $(docker compose ps -q worker | head -1)
  ```

  O broker reentrega a mensagem dele a outro Worker. O `docker kill` conta como parada manual, então o Compose não o reinicia; o próximo comando o traz de volta.
- Escale: `docker compose up -d --no-build --scale worker=4`. No Grafana, a fila passa a escoar mais rápido.
- O pico termina: **20 concluídos, 0 falhos**. Medido: 76,6 s com 2 Workers e 47,8 s com 4, sem queda.

### 8:00 — Funcionando: broker fora do ar (45 s)

- `docker compose stop rabbitmq`
- Postman, **Enviar vídeo** com o vídeo bom: continua `202`, e o Vídeo fica em `recebido`. O pedido está na outbox.
- `docker compose start rabbitmq`
- **Aguardar conclusão**: `concluido` em cerca de 15 s.

### 8:45 — Qualidade e CI/CD (45 s)

- GitHub Actions: CI com lint, `go test -race` (Postgres, RabbitMQ e Redis reais), govulncheck e Trivy.
- CD: publica as imagens, sobe a stack no runner, roda a coleção do Postman e recria o RabbitMQ com um Vídeo na fila. Só então a tag `main` avança.
- Pacotes no GHCR e a tag de versão.

### 9:30 — Fechamento (15 s)

- Requisitos obrigatórios atendidos; o link do repositório.

## Se algo der errado

| Sintoma | Causa provável | Saída |
|---|---|---|
| `429` no cadastro | Limite por IP | `docker compose restart redis` zera os contadores |
| `429` no envio | Cota de 5 Vídeos em andamento do Usuário | Espere terminar, ou refaça Cadastro e Login |
| Fila não aparece no Grafana | Coleta a cada 15 s | Espere duas coletas |
| `401` nas rotas de Vídeo | Token de 1 h expirou | Rode o Login de novo |
