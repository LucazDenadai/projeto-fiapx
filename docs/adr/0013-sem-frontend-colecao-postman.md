---
status: accepted
resolve: E01, E04, A12
---

# ADR-0013 — Sem frontend: a API é usada por uma coleção do Postman versionada

## Contexto

O projeto base serve uma página HTML na raiz (`legacy/main.go:51-54`). O enunciado pede que o Usuário consiga enviar um vídeo e baixar o zip, sem dizer por qual interface. Os entregáveis são a documentação da arquitetura (E01), os scripts, o repositório e um vídeo de até 10 min mostrando o projeto funcionando (E04); nenhum deles exige tela. O desafio avalia arquitetura, microsserviços, mensageria e qualidade. O [ADR-0012](0012-ci-cd-github-actions-ghcr.md) prevê um smoke test ponta a ponta no CD.

## Decisão

**O sistema não tem frontend. A API só serve JSON e arquivos. Uma coleção do Postman versionada é a interface do vídeo de entrega, a documentação da API e o smoke test do CD.**

- **Coleção e ambiente versionados** em `docs/api/`:
  - uma request por rota (`POST /usuarios`, `POST /login`, `POST /videos`, `GET /videos`, `GET /videos/{id}`, `GET /videos/{id}/zip`);
  - na descrição de cada request, os códigos de resposta (`400`, `401`, `404`, `409`, `413`, `415`, `429`) e os Motivos da falha. A coleção é a documentação da API (E01);
  - o script de teste do `POST /login` salva o token numa variável, e as outras requests mandam o `Authorization: Bearer` sozinhas;
  - o ambiente tem `base_url` e senhas de exemplo, sem segredo real.
- **Smoke test do CD = a mesma coleção**, rodada com o **newman** no runner (ADR-0012). Os passos são cadastro, login, envio do vídeo gerado pelo ffmpeg (`testsrc`), polling até `concluido` e download do zip. Os testes da coleção conferem os status e o conteúdo. O fluxo do vídeo e o do CD são o mesmo: se a coleção quebrar, o CD acusa.
- **A API não serve HTML**: a página do projeto base não é recriada. Como não há navegador nem outra origem, a decisão "sem CORS" do [ADR-0007](0007-autenticacao-jwt-na-api.md) segue sem exceção.

## Opções consideradas

- **Página HTML simples servida pela API (`go:embed`, JS puro)**: rejeitado. Exige CSP, cuidado com XSS a partir do nome do arquivo, token em memória do navegador e download por blob, que passa o zip inteiro pela memória. Tudo isso para uma tela que nenhum requisito pede.
- **SPA (React ou Vue) num container próprio**: rejeitado. Teria build, Node no CI, outra origem e CORS, sem requisito que peça.
- **OpenAPI (`openapi.yaml`) como documentação**: rejeitado. Manter OpenAPI e coleção duplicaria a descrição das rotas. A coleção documenta, demonstra e testa ao mesmo tempo.
- **Smoke test do CD num script próprio (`curl` + `jq`)**: rejeitado. Seria um segundo roteiro do mesmo fluxo, que podia divergir da coleção usada no vídeo.

## Consequências

- No vídeo, o "projeto funcionando" aparece como requisições no Postman. O visual de escala e falha vem do Grafana (ADR-0010) e do Mailpit (ADR-0008).
- Toda rota nova ou mudança de contrato atualiza a coleção. Como o CD roda a coleção, uma coleção desatualizada quebra o CD.
- O CD precisa do newman no runner (via `npx` ou imagem `postman/newman`).
- Um frontend pode ser somado depois, como cliente da mesma API. Nesse caso, CSP, XSS e CORS voltam a ser decididos.
