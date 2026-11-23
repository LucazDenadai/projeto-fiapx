---
status: accepted
resolve: RT04, E02
---

# ADR-0016 — Testes de integração com testcontainers-go e migrations com goose

## Contexto

RT04 exige testes que garantam a qualidade. E02 exige o script de criação do banco e dos demais recursos. O `CLAUDE.md` define "pronto" como `gofmt`, `go vet ./...` e `go test -race ./...` verdes. Os ADRs anteriores já pedem testes contra dependências reais:

- Postgres: outbox com `SKIP LOCKED` ([ADR-0003](0003-transactional-outbox.md)) e cota com lock ([ADR-0011](0011-escala-e-limites.md));
- RabbitMQ: quorum queues, `delivery-limit` e DLQ ([ADR-0004](0004-retry-e-dlq.md));
- Redis: limite de tentativas e fail-open ([ADR-0009](0009-redis-limite-de-tentativas.md));
- ffmpeg: duração e timeout ([ADR-0011](0011-escala-e-limites.md)).

O [ADR-0012](0012-ci-cd-github-actions-ghcr.md) roda o CI com Docker no runner. O [ADR-0013](0013-sem-frontend-colecao-postman.md) faz do newman o teste ponta a ponta.

## Decisão

**Testes de integração com containers reais criados pelo próprio `go test` (testcontainers-go). Migrations em SQL, só para a frente, com goose, aplicadas por um serviço de execução única.**

- **testcontainers-go:** cada pacote que fala com Postgres, RabbitMQ ou Redis sobe um container descartável, um por pacote, com as mesmas versões de imagem do `docker-compose.yml`. O container sobe no primeiro teste que o pede e o `TestMain` do pacote o derruba no fim (helpers em `internal/integracao`). Os testes isolam o estado entre si: schema, vhost ou prefixo de chave por teste.
- **Rodam por padrão:** `go test -race ./...` inclui a integração, então a regra de "pronto" do `CLAUDE.md` a cobre. Com `-short`, os testes de integração são pulados, para o ciclo rápido do TDD. Sem build tags.
- **ffmpeg real:** os testes do Worker usam o ffmpeg do PATH, com vídeo gerado na hora (`testsrc`), sem binário versionado. Sem ffmpeg no PATH, o teste **falha** com uma mensagem dizendo como instalar (`winget install ffmpeg`, `apt install ffmpeg`). O CI instala o ffmpeg.
- **Camadas de teste:**
  - unitários: domínio e regras puras, sem I/O;
  - integração: testcontainers-go, mais o `t.TempDir()` como volume (ADR-0005) e um SMTP falso em memória ([ADR-0008](0008-notificacao-por-email.md));
  - ponta a ponta: a coleção do Postman com o newman contra a stack do Compose, no CD (ADR-0012, ADR-0013).
- **Migrations com goose:** arquivos `.sql` versionados em `migrations/`, embutidos no binário da API (`embed`), com o goose usado como biblioteca.
  - São **só para a frente**, sem `down`. Para corrigir, escreve-se uma migration nova.
  - Toda migration precisa ser compatível com a versão de API e Worker em execução (expandir antes, contrair depois), porque o banco é compartilhado (ADR-0001).
- **Quem aplica:** o serviço **`migrate`** do Compose, de execução única, que roda `api migrate` com a imagem da API. API e Worker sobem só depois dele (`depends_on: service_completed_successfully`). Os SQL mais esse serviço são o script de criação do banco do E02. Os testes de integração aplicam as mesmas migrations no Postgres do testcontainer, então toda migration é testada a cada `go test`.
- **Cobertura:** o CI gera `-coverprofile` e publica o total no resumo do job. Não bloqueia por porcentagem mínima.

## Opções consideradas

- **`docker compose up` no CI e testes contra a stack**: rejeitado. O estado fica compartilhado entre os testes, há uma etapa a mais antes do `go test`, e rodar um teste sozinho exige subir a stack inteira.
- **Integração atrás de build tag (`//go:build integration`)**: rejeitado. O `go test ./...` padrão ficaria verde sem testar o banco, esvaziando a regra de "pronto".
- **Pular os testes do ffmpeg quando ele não está instalado**: rejeitado. Um "verde" que não rodou o ffmpeg esconde a parte mais arriscada do sistema.
- **Mocks de Postgres e RabbitMQ**: rejeitados para a integração. `SKIP LOCKED`, `FOR UPDATE`, `delivery-limit` e dead-letter são comportamentos do servidor real. Mocks continuam valendo nas interfaces de domínio.
- **golang-migrate**: rejeitado. Exige pares de arquivos `up` e `down` e é menos direto como biblioteca.
- **Atlas (migrations declarativas)**: rejeitado. Poderoso demais para um schema de poucas tabelas.
- **Migrations com `down`**: rejeitado. O `down` raramente é testado e, num banco compartilhado, voltar o schema quebra a versão em execução.
- **A API migrar ao subir**: rejeitado. As réplicas disputariam as migrations, e o Worker dependeria da API para ter o schema.
- **Cobertura mínima obrigatória**: rejeitado. Incentiva testes que só passam pelas linhas, sem verificar nada.

## Consequências

- `go test ./...` exige Docker e ffmpeg na máquina. A primeira execução baixa as imagens; as seguintes reaproveitam o cache.
- O CI fica mais lento que um CI só com testes unitários. Containers por pacote rodam em paralelo, e `-short` fica para o ciclo local.
- No Windows, o testcontainers-go precisa do Docker Desktop rodando.
- O schema evolui só por migration nova. O `migrate` falhando impede a subida da API e do Worker, o que é intencional.
