---
status: accepted
resolve: RF04, S03, A7
---

# ADR-0009 — Redis só para o limite de tentativas de login e cadastro

## Contexto

A stack recomendada sugere "PostgreSQL + Redis (cache)" (S03), sem exigir. O [ADR-0007](0007-autenticacao-jwt-na-api.md) limita as tentativas em `POST /login` e `POST /usuarios` e deixou aberto onde fica o contador. A API pode ter mais de uma réplica (RT02, A9). Um contador na memória de cada réplica multiplica o limite pelo número de réplicas e zera a cada reinício.

## Decisão

**Um Redis no Compose, usado só para os contadores do limite de tentativas. Nada de cache.**

- **Contadores por janela fixa:** `INCR` na chave, e `EXPIRE … NX` com a duração da janela, os dois no mesmo pipeline. As chaves são `limite:login:email:<email>`, `limite:login:ip:<ip>` e `limite:cadastro:ip:<ip>`.
- **Valores padrão**, configuráveis por variável de ambiente:
  - login: 5 falhas por e-mail em 15 min; 20 tentativas por IP por minuto;
  - cadastro: 5 por IP por hora.

  Um login bem-sucedido apaga o contador daquele e-mail. Estourou o limite: `429` com `Retry-After`.
- **No login, o contador do e-mail soma toda tentativa, antes de conferir a senha.** Como o sucesso apaga o contador, o efeito é o de "5 falhas seguidas". Contar só depois da falha deixaria tentativas paralelas passarem do limite antes de o contador subir. E-mail vazio não tem contador.
- **IP do cliente:** vem de `RemoteAddr`. A API é exposta direto, sem proxy reverso, então `X-Forwarded-For` é ignorado, porque o cliente poderia forjá-lo.
- **Redis fora do ar: deixar passar sem limite (fail-open).** Login e cadastro seguem sem limite, com log de erro e métrica. O Redis é proteção auxiliar; a fonte de verdade é o Postgres. A chamada ao Redis tem timeout curto (200 ms, configurável), para não segurar o login; o cliente go-redis precisa de `ContextTimeoutEnabled`, senão ignora o prazo do contexto. A métrica das falhas do Redis entra com o Prometheus (ADR-0010).
- **Redis efêmero e interno:** sem persistência (nem RDB nem AOF), com `maxmemory` e política `volatile-ttl`. Sem porta exposta fora da rede do Compose. Perder os dados num reinício só zera as janelas.
- **Sem cache da listagem (RF04):** a listagem consulta o Postgres direto, por `usuario_id` com índice.

## Opções consideradas

- **Contador na memória de cada réplica da API**: rejeitado. Com N réplicas o limite real vira N vezes o configurado, e o contador zera a cada reinício.
- **Contador no Postgres**: rejeitado. Põe uma escrita no banco a cada tentativa de login, justamente no caminho que um ataque de força bruta martela, e exige limpar as janelas vencidas.
- **Bloquear login e cadastro com o Redis fora (fail-closed)**: rejeitado. O Redis viraria ponto único de falha do acesso ao sistema.
- **Cache da listagem de status**: rejeitado. É uma consulta indexada de poucas linhas. O cache teria de ser invalidado a cada mudança de status feita pelo Worker; se a invalidação falhar, o Usuário vê `processando` depois de o Vídeo já estar `concluido`. Não há medição que peça cache.
- **Não usar Redis**: rejeitado. Sem ele, o limite fica errado assim que a API tiver mais de uma réplica.
- **Janela deslizante ou token bucket**: rejeitado. É mais preciso na borda da janela, mas custa um script Lua ou estruturas ordenadas, sem ganho que importe para um limite anti-força-bruta.

## Consequências

- O Compose ganha o serviço `redis`, sem volume.
- Com o Redis fora, não há limite de tentativas até ele voltar. O alerta vem pela métrica (A8).
- Usuários atrás do mesmo IP (NAT) compartilham o limite por IP. O limite por e-mail continua individual.
- O limite por e-mail permite a um atacante bloquear o login de uma vítima por 15 min. Esse risco é aceito, porque a janela é curta e configurável.
- Os testes de integração do limite rodam contra um Redis real (container de teste). O fail-open é testado com o Redis fora do ar (conexão recusada) e travado (aceita a conexão e não responde).
- Mudanças nesse código passam pelo `/security-review` (regra do `CLAUDE.md`: auth).
