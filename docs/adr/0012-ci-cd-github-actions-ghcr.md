---
status: accepted
resolve: RT05, A10
---

# ADR-0012 — CI/CD: GitHub Actions, imagens no GHCR e deploy efêmero no runner

## Contexto

RT05 exige CI/CD. A plataforma é Docker Compose num host, e nuvem e Kubernetes estão fora de escopo (matriz §6). Não há um servidor de destino para um deploy contínuo tradicional. O projeto base não tem CI, e o Dockerfile dele roda `go run` como root, com o toolchain inteiro na imagem (Q09). O [ADR-0001](0001-api-worker-notifier.md) define três imagens: API, Worker e Notifier.

## Decisão

**CI em todo PR e push na `main`. CD a cada merge: publicar as imagens no GHCR e implantar a stack inteira num ambiente efêmero dentro do runner, validado por um smoke test.**

- **CI** (GitHub Actions, em PR e push na `main`):
  - `gofmt -l` (falha se houver diferença), `go vet ./...`, golangci-lint;
  - `go test -race ./...`;
  - `govulncheck ./...` (vulnerabilidades conhecidas nas dependências);
  - build das 3 imagens e varredura com **Trivy**, que falha com vulnerabilidade `CRITICAL` que já tenha correção.
- **CD** (merge na `main`, depois do CI verde):
  1. publica as 3 imagens no **GHCR** (`ghcr.io/lucazdenadai/fiapx-{api,worker,notifier}`), autenticado pelo `GITHUB_TOKEN` com permissão `packages: write`;
  2. sobe o `docker-compose.yml` no runner **com as imagens recém-publicadas**;
  3. roda o **smoke test**: cadastro, login, envio de um vídeo curto gerado na hora pelo ffmpeg (`testsrc`, sem binário versionado), espera até `concluido`, download do zip e conferência de que ele tem frames.

  As imagens publicadas passam de novo pelo Trivy. Com falha no smoke ou no Trivy, o workflow fica vermelho e a tag `main` da imagem não avança. Ela também não avança se a `main` já estiver em outro commit: uma execução antiga não a leva para trás.
- **Tags das imagens:** `sha-<commit>` e `main` a cada merge; `X.Y.Z` a cada tag git `vX.Y.Z`, que promove a imagem `sha-<commit>` já publicada, depois de repetir o smoke test, sem build novo. Sem `latest`. O `docker-compose.yml` usa `image: ghcr.io/…:${TAG:-main}`, então implantar em qualquer máquina com Docker é clonar o repositório, criar o `.env` e rodar `docker compose pull && docker compose up -d --no-build`: sem Go e sem build. O clone traz os arquivos que o Compose monta no Prometheus e no Grafana ([ADR-0010](0010-observabilidade-prometheus-grafana.md)). Para desenvolvimento local, um `compose.override.yml` builda as imagens do código.
- **Imagens (fecha Q09):** multi-stage, com build estático (`CGO_ENABLED=0`) e usuário não-root.
  - API e Notifier: base **distroless**.
  - Worker: base mínima **com ffmpeg** (o distroless não tem ffmpeg).
  - Sem `go run`, sem toolchain e sem `COPY . .` na imagem final.
- **Proteção da `main`:** a partir do primeiro código de aplicação, mudança de código entra por PR com o CI como check obrigatório. Docs e ADRs podem ir direto para a `main`. O GitHub não separa a proteção por pasta: a regra vale para tudo, e o administrador a pula só para docs.

## Opções consideradas

- **Deploy automático numa VM por SSH**: rejeitado. Exige uma VM paga, segredos de acesso e uma máquina para manter, e a nuvem está fora de escopo. O deploy efêmero prova a mesma coisa (as imagens publicadas sobem e funcionam) a cada merge.
- **Só build e testes (sem CD)**: rejeitado. Nada é entregue, e o "CD" de RT05 fica sem resposta.
- **Docker Hub como registry**: rejeitado. Exige conta e token à parte. O GHCR usa o `GITHUB_TOKEN` e fica ao lado do código.
- **Tag `latest`**: rejeitado. Esconde qual versão está rodando e dificulta voltar para uma versão anterior.
- **Base Alpine para todas as imagens**: rejeitado para API e Notifier. A distroless tem menos superfície (sem shell nem gerenciador de pacotes). O Worker fica numa base mínima só porque precisa do ffmpeg.
- **Commit direto na `main` também para código**: rejeitado. Código sem CI verde poderia chegar na `main` e no GHCR.

## Consequências

- O repositório ganha `.github/workflows/` (CI e CD), Dockerfiles por serviço, o `compose.override.yml` e o script do smoke test.
- O smoke test valida o fluxo inteiro a cada merge, mas deixa o CD mais lento (subir Postgres, RabbitMQ, Redis, Mailpit, Prometheus, Grafana e os 3 serviços). É aceito, porque roda só no merge e na tag de versão, não em todo PR.
- O smoke test não substitui os testes de integração ([ADR-0016](0016-testes-de-integracao-e-migrations.md)).
- As imagens no GHCR precisam ser públicas, ou o `docker compose pull` fora do runner exige login no GHCR.
- O Trivy pode quebrar o CI por uma CVE nova na imagem base, sem mudança no código. A correção é atualizar a base.
- Fecha Q09 (Dockerfile de produção com boas práticas).
