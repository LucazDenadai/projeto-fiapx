---
status: accepted
resolve: RF01, RF04, RT02, A1
---

# ADR-0001 — Três processos: API, Worker e Notifier

## Contexto

O projeto base (`legacy/main.go`) é um processo único que recebe o upload e roda o ffmpeg dentro da própria requisição HTTP (`main.go:117`). Um vídeo longo segura a conexão, não há paralelismo controlado, o trabalho se perde se o processo cai e não há como escalar o Processamento sem escalar tudo. Também serve `outputs/` como estático público (Q01) e baixa arquivos sem checar dono (Q02).

## Decisão

O sistema tem **três processos**, num só repositório e módulo Go (`cmd/api`, `cmd/worker`, `cmd/notifier`, domínio em `internal/`), cada um com sua imagem Docker:

- **API**: único ponto de entrada do Usuário. Autentica (módulo interno, mecanismo em A5), recebe o Vídeo, cria o registro, enfileira o Processamento, lista o status e entrega o Zip de frames depois de checar o dono.
- **Worker**: consome a fila, executa o Processamento (ffmpeg → Zip de frames) e avança o status do Vídeo. Escala horizontalmente por réplicas competindo na fila.
- **Notifier**: consome o evento de falha definitiva do Processamento e notifica o Usuário (meio em A6).

API e Worker **compartilham o mesmo Postgres**: a API cria o Vídeo; só o Worker avança o status do Processamento. Worker e Notifier não expõem porta fora da rede interna do Compose; se bytes de upload/download forem direto ao storage (A11), é a API quem emite a autorização, após checar o dono.

## Opções consideradas

- **Processo único com pool de goroutines**: rejeitado. Atende RF01 parcialmente, mas perde o trabalho em andamento numa queda (RF02) e não escala o Processamento separado da API (RT02).
- **API + Worker, notificação dentro do Worker**: rejeitado. Falha do servidor de notificação se mistura à do Processamento: ou trava o Worker, ou a notificação se perde em silêncio.
- **Serviço de autenticação separado**: rejeitado. Um salto de rede, uma imagem e um deploy a mais sem requisito que o peça; fica como pacote da API.
- **Banco por serviço (Worker publica status, só a API grava)**: rejeitado. Exige outra fila e traz consistência eventual na listagem (RF04) sem ganho; API e Worker são o mesmo contexto de domínio.
- **Um módulo Go por serviço**: rejeitado. Versionamento independente não compensa num projeto individual; duplicaria tipos e complicaria o CI.

## Consequências

- Upload responde rápido; o Processamento é assíncrono e o Usuário acompanha pelo status (RF04).
- O mecanismo de fila entre API → Worker e Worker → Notifier fica para A2; a consistência "gravar Vídeo + enfileirar" para A3.
- Banco compartilhado acopla API e Worker ao mesmo schema: migrations versionadas valem para os dois e precisam ser compatíveis com versões em execução durante o deploy.
- Três imagens para construir e publicar no CI (A10); `--scale worker=N` é o eixo de escala demonstrado (A9).
- Fecha Q01 e Q02 por construção: nada é servido sem passar pela checagem de dono da API.
