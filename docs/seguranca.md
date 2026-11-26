# Segurança: OWASP e limitações conhecidas

Situação da API frente ao [OWASP API Security Top 10 (2023)](https://owasp.org/API-Security/editions/2023/en/0x11-t10/), o resultado da varredura dinâmica com o OWASP ZAP e o que fica de fora. OWASP não é certificação: isto mostra como cada categoria é tratada e com que evidência.

## OWASP API Security Top 10 (2023)

| Categoria | Situação | Como e onde |
|---|---|---|
| API1 Autorização por objeto | Coberto | Vídeo ou zip de outro Usuário dá `404`, igual a inexistente. `TestConsulta_VideoAlheioDa404IgualAInexistente`, `TestDownload_ZipDeOutroUsuarioDa404` |
| API2 Autenticação | Coberto | Hash argon2id com os parâmetros da OWASP; JWT só HS256, com validade de 1 h; `401` genérico no login; limite de tentativas por e-mail e por IP ([ADR-0007](adr/0007-autenticacao-jwt-na-api.md), [ADR-0009](adr/0009-redis-limite-de-tentativas.md)). `TestRotaDeVideo_RecusaTokenAusenteExpiradoOuComOutroAlgoritmo`, `TestLimite_*` |
| API3 Propriedades do objeto | Coberto | Respostas montadas campo a campo: sem e-mail, caminho de arquivo, tentativas ou saída do ffmpeg. `TestListagem_FalhouMostraSoCodigoETextoDoMotivo`, `TestProcessar_SaidaDoFFmpegSoNoLog` |
| API4 Consumo de recursos | Parcial | Teto de tamanho, prazo de leitura, duração do vídeo, timeout do ffmpeg, Cota, CPU e memória por container ([ADR-0011](adr/0011-escala-e-limites.md)). Falta limite de requisições na listagem e no download (abaixo) |
| API5 Autorização por função | Não se aplica | Não há papéis nem rotas administrativas |
| API6 Fluxos de negócio | Coberto | Cota de Vídeos em andamento e limite de cadastros por IP. `TestCota_EnviosSimultaneosSoUmPassa` |
| API7 SSRF | Não se aplica | A API não busca URLs informadas pelo cliente |
| API8 Configuração | Parcial | Imagens distroless e não-root; sem CORS; métricas e saúde fora da porta pública; `nosniff`, `Cross-Origin-Resource-Policy` e `Cache-Control: no-store` em toda resposta (`TestCabecalhos_DeSegurancaEmTodaResposta`). Falta TLS (abaixo) |
| API9 Inventário | Coberto | Todas as rotas na [coleção do Postman](api/); imagens versionadas por tag, sem `latest` |
| API10 Consumo de APIs de terceiros | Coberto | Só o SMTP, com timeout; o e-mail é montado sem injeção de cabeçalho (`TestMontar_NomeComQuebraDeLinhaNaoCriaCabecalho`) |

Fora da lista: SQL sempre parametrizado; caminho de arquivo gerado do `video_id` validado como UUID, nunca do nome enviado; ffmpeg com `exec.CommandContext`, timeout e formatos de entrada restritos. No CI rodam o govulncheck (dependências Go) e o Trivy (imagens); o Dependabot avisa de dependência vulnerável e abre o PR de correção.

## Varredura dinâmica (OWASP ZAP)

`zap-api-scan` (varredura passiva e ativa) com um token válido, contra a stack do Compose com as imagens `1.0.1`, em 2026-11-26. Rodou com os limites padrão e depois com os limites de tentativas e a Cota afrouxados, para os ataques chegarem ao código em vez de pararem no `429`. As duas primeiras rodadas deram 118 regras sem achado e 3 avisos. Corrigidos os avisos, a terceira rodada, já com a API corrigida, deu:

| Resultado | Quantidade |
|---|---|
| Regras sem achado | 120 |
| Falhas | 0 |
| Respostas `500` ou erro no log | 0 |
| Avisos | 1 (falso positivo) |

| Aviso das primeiras rodadas | Tratamento |
|---|---|
| X-Content-Type-Options ausente (JSON) | Corrigido: o cabeçalho vai em toda resposta |
| Cross-Origin-Resource-Policy ausente | Corrigido: `same-origin` em toda resposta |
| Content-Type inesperado no download | Falso positivo: o zip é `application/zip` de propósito |

Para repetir: suba a stack, gere um token pelo login e rode o `zaproxy/zap-stable` com `zap-api-scan.py`, na rede do Compose. O ZAP precisa de um OpenAPI com as 6 rotas da coleção. O projeto não versiona um ([ADR-0013](adr/0013-sem-frontend-colecao-postman.md)), então o da varredura foi escrito à parte.

## Limitações conhecidas

- **Sem TLS.** A API fala HTTP puro, e senha e token trafegam sem criptografia. É aceitável no Compose local, onde cliente e API estão na mesma máquina. Um deploy real precisa terminar HTTPS na frente da API, num proxy reverso ou balanceador, e só ele expõe porta. Grafana e Mailpit já atendem só em `127.0.0.1`.
- **Sem limite de requisições na listagem, na consulta e no download.** Só login, cadastro e envio são limitados. Quem tem um token válido pode fazer muitas leituras; o custo de cada uma é pequeno (consulta por índice ou leitura em streaming do volume).
- **Token não é revogável.** Vale até expirar (1 h); não há logout nem lista de revogação (fora de escopo na spec #18).
- **Sem DAST no CI.** A varredura com o ZAP foi manual; o CD roda a coleção do Postman, mas não ataques.
