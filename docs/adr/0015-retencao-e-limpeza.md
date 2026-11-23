---
status: accepted
resolve: RT01, RT02
---

# ADR-0015 — Retenção: original apagado ao terminar, zip por 7 dias, limpador no Worker

## Contexto

O volume do [ADR-0005](0005-volume-arquivos.md) só cresce se nada apagar o que ele guarda. O que acumula:

- originais de até 500 MB e zips de alguns GB ([ADR-0011](0011-escala-e-limites.md));
- arquivos sem Vídeo aceito, quando há queda entre gravar e confirmar ([ADR-0003](0003-transactional-outbox.md));
- temporários de gravação deixados por quedas ([ADR-0005](0005-volume-arquivos.md), [ADR-0006](0006-envio-e-download-pela-api.md));
- linhas já publicadas da `outbox` (ADR-0003).

O [ADR-0014](0014-ciclo-de-vida-do-video.md) tirou o reprocessamento, então o original não serve para nada depois que o Vídeo termina.

## Decisão

**O original é apagado quando o Vídeo termina. O Zip de frames fica disponível por 7 dias. Um limpador periódico no Worker recolhe o resto.** Todos os prazos e intervalos são configuráveis por variável de ambiente.

- **Original:** o Worker apaga `/data/videos/{video_id}/original` logo depois de confirmar a transição para `concluido` ou `falhou`. Se a remoção falhar (queda, erro de I/O), o limpador recolhe depois. A falha é só registrada no log, sem afetar o status.
- **Zip de frames: 7 dias** depois de `concluido`. O Vídeo ganha `zip_expira_em`, preenchido na transição para `concluido`. O status continua `concluido`: os quatro estados do ADR-0014 não mudam. Depois da expiração:
  - a listagem não mostra mais o `download`, e mostra o `zip_expira_em`;
  - `GET /videos/{id}/zip` responde **`410 Gone`**. Os códigos do ADR-0006 continuam valendo para o resto: `404` para Vídeo de outro Usuário, `409` para Vídeo não concluído.
- **Limpador: goroutine no Worker**, a cada 1h. Antes de cada rodada, tenta `pg_try_advisory_lock`; se outra réplica já tem o lock, pula a rodada. Cada rodada recolhe:
  1. zips com `zip_expira_em` vencido;
  2. originais de Vídeos terminais que ainda existem;
  3. temporários de gravação no volume com mais de 1h (acima do timeout de envio de 10 min);
  4. diretórios `/data/videos/{id}` sem Vídeo no banco, com mais de 1h (queda entre gravar e confirmar);
  5. linhas da `outbox` com `publicado_em` de mais de 7 dias, em lotes de 1000. Linha não publicada nunca é apagada.

  Os temporários locais de cada Worker continuam sendo limpos na subida dele (ADR-0005).
- **O registro do Vídeo no banco fica para sempre:** é pequeno e é o histórico do Usuário na listagem.

## Opções consideradas

- **Manter o original enquanto existir o zip**: rejeitado. Sem reprocessamento (ADR-0014), o original não tem uso e é o maior arquivo.
- **Estado `expirado`**: rejeitado. Exigiria substituir o ADR-0014 por uma informação que o `zip_expira_em` já dá. `410 Gone` comunica a expiração sem estado novo.
- **Zip sem expiração**: rejeitado. O volume cresceria sem limite; a cota limita só os Vídeos em andamento.
- **Serviço `limpador` separado, ou cron no host**: rejeitado. Seria mais um container ou uma dependência fora do Compose, para uma tarefa de segundos por hora. O Worker já monta o volume e acessa o banco.
- **Limpador em todas as réplicas, sem lock**: rejeitado. Réplicas apagariam os mesmos arquivos em paralelo e encheriam o log de erros de "arquivo não existe".
- **Lifecycle do sistema de arquivos ou do Docker**: rejeitado. Não existe para volumes nomeados.
- **Apagar o registro do Vídeo junto com o zip**: rejeitado. O Usuário perderia o histórico, e a listagem mostraria menos do que ele enviou.

## Consequências

- O disco ocupado fica limitado a: originais dos Vídeos em andamento (cota de 5 por Usuário), mais os zips dos últimos 7 dias, mais temporários de até 1h.
- A tabela de Vídeos ganha `zip_expira_em`, com índice para a busca do limpador. A coleção do Postman documenta o `410`.
- O limpador ganha métricas (ADR-0010): itens apagados por tipo e duração da rodada.
- Com `--scale worker=N`, só uma réplica limpa por rodada. Se ela cair no meio, o lock é liberado com a conexão, e a próxima rodada continua do ponto em que parou.
- Os testes cobrem cada item da rodada com relógio injetado, e o lock com duas instâncias concorrentes.
- Apagar Usuários e os dados deles (LGPD) fica fora do escopo.
