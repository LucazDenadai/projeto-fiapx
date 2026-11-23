---
status: accepted
resolve: RF03, A5
---

# ADR-0007 — Cadastro e login por e-mail e senha na própria API, com JWT

## Contexto

RF03 exige que o sistema seja "protegido por usuário e senha". O projeto base não tem autenticação nem noção de dono, e libera CORS `*` (`legacy/main.go:36`). O [ADR-0001](0001-api-worker-notifier.md) põe a autenticação num módulo interno da API, sem serviço separado. Os ADRs [0001](0001-api-worker-notifier.md) e [0006](0006-envio-e-download-pela-api.md) dependem de saber quem é o Usuário de cada requisição para checar o dono.

## Decisão

**Credenciais na própria API (`internal/auth`), cadastro aberto, login por e-mail e senha, JWT de curta duração.**

- **Cadastro aberto: `POST /usuarios`**, com `{email, senha}`, sem autenticação. A resposta é `201`. Se o e-mail já está cadastrado, a resposta é `409`. Não há papéis nem administrador: todo Usuário tem as mesmas permissões. As migrations criam só o schema; não há Usuários pré-criados.
- **Identificador: e-mail**, normalizado em minúsculas, único. O mesmo e-mail serve ao A6, se a notificação for por e-mail.
- **Hash: argon2id** (`golang.org/x/crypto/argon2`) com os parâmetros recomendados pelo OWASP e sal por Usuário, gravado em formato PHC (`$argon2id$v=19$m=…,t=…,p=…$sal$hash`), com os parâmetros junto do hash.
- **Regra de senha no cadastro:** de 8 a 128 caracteres, com pelo menos uma letra maiúscula, uma minúscula, um número e um caractere especial. Senha fora da regra: `400`, com a mensagem do que falta.
- **Login: `POST /login`**, com `{email, senha}`, devolve `{token, expira_em}`.
  - E-mail inexistente e senha errada respondem o mesmo `401 credenciais inválidas`.
  - Para e-mail inexistente, a API compara a senha com um hash fictício. Assim o tempo de resposta não revela se o e-mail existe.
- **Token: JWT HS256**, com segredo vindo de variável de ambiente (`JWT_SECRET`), de no mínimo 32 bytes (256 bits, o tamanho da saída do HMAC-SHA256).
  - Claims: `sub` (id do Usuário), `iat` e `exp` (1h). Sem refresh token: expirou, faz login de novo.
  - A validação exige o algoritmo HS256 explicitamente, o que rejeita `alg: none` e troca de algoritmo, e confere o `exp`.
- **Transporte: `Authorization: Bearer <token>`** em todas as rotas de Vídeo. Sem token ou com token inválido: `401`. O id do Usuário vem do token, nunca do corpo ou da URL.
- **Limite de tentativas em `POST /login` e `POST /usuarios`**, por IP e por e-mail, com resposta `429`. Onde o contador fica (memória de cada réplica ou Redis) decide o A7.
- **Sem CORS.** A API e a eventual página do A12 ficam na mesma origem. O `*` do projeto base some.

## Opções consideradas

- **IdP (Keycloak)**: rejeitado. Um container a mais, com banco e realm para configurar, e OIDC para explicar no vídeo, sem requisito que peça mais que usuário e senha.
- **Usuários pré-criados por migration**: rejeitado. Os hashes ficariam versionados e as senhas públicas no README, e criar um Usuário novo exigiria outra migration. O cadastro aberto é mais simples de demonstrar.
- **Administrador que cria Usuários, ou convida por e-mail (o Usuário define a própria senha com um código de uso único)**: rejeitado. O convite dá um papel claro ao admin e evita a enumeração de e-mail, mas custa papéis, um admin inicial, uma tabela de convites com expiração e a entrega do código. Nenhum requisito pede controle de quem entra.
- **Token opaco de sessão no Postgres**: rejeitado. O logout revogaria de verdade, mas ao custo de uma consulta ao banco por requisição. A revogação não é requisito.
- **JWT com refresh token**: rejeitado. Rotação e revogação do refresh são código a mais sem requisito. Uma expiração de 1h basta para o uso.
- **Cookie `HttpOnly`**: rejeitado. Exige proteção contra CSRF. O header Bearer não tem esse risco e atende tanto à página quanto ao Postman.
- **Só tamanho mínimo + lista de senhas comuns proibidas (NIST SP 800-63B)**: rejeitado. É a recomendação atual do NIST, que desaconselha regras de composição. Preferimos as regras de composição por serem a política que o usuário e o avaliador reconhecem de imediato.
- **bcrypt**: rejeitado. Trunca a senha em 72 bytes. O argon2id é a primeira recomendação do OWASP.
- **Nome de usuário em vez de e-mail**: rejeitado. Seria um segundo identificador, e o A6 provavelmente precisa do e-mail.

## Consequências

- Qualquer pessoa com acesso à API pode criar conta. O `409` revela quais e-mails já estão cadastrados. Esse risco é aceito e mitigado pelo limite de tentativas.
- Não há verificação de e-mail: um Usuário pode se cadastrar com um e-mail que não é dele. Se o A6 notificar por e-mail, a notificação vai para esse endereço.
- As regras de composição aceitam senhas previsíveis como `Senha@123`.
- O logout é só do cliente (descartar o token). Um token vazado vale até expirar, em no máximo 1h. Trocar o segredo invalida todos os tokens.
- No vídeo, dois Usuários se cadastram pela API e mostram o isolamento: a listagem vem filtrada por Usuário e o download do Vídeo de outro Usuário dá `404` (ADR-0006).
- O segredo do JWT é obrigatório na subida da API: ausente ou com menos de 32 bytes, a API não sobe. Um segredo curto seria quebrável por força bruta offline a partir de qualquer token emitido.
- Mudanças em `internal/auth` passam pelo `/security-review` (regra do `CLAUDE.md`).
- O limite de tentativas depende do A7 para definir onde o contador fica.
