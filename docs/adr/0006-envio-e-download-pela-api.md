---
status: accepted
resolve: RF01, RT02, A11
---

# ADR-0006 — Envio e download dos arquivos pela API, em streaming

## Contexto

O [ADR-0005](0005-volume-arquivos.md) guarda vídeos e zips num volume do Docker, sem object storage. Sem object storage não há URL pré-assinada: os bytes de envio e de download passam pela API. No projeto base, o upload usa `FormFile`, que bufferiza o arquivo em memória e em temporário (`legacy/main.go:76`). Não há limite de tamanho (Q05). A validação olha só a extensão (Q04). O download não checa o dono (Q02).

## Decisão

**Envio: `POST /videos`, `multipart/form-data` lido em streaming, um Vídeo por requisição.**

- A API lê o corpo com `r.MultipartReader()`, sem `FormFile`/`ParseMultipartForm`. A parte `video` é copiada direto para um temporário no volume e publicada com `os.Rename` em `/data/videos/{video_id}/original` (ADR-0005). A memória gasta não depende do tamanho do arquivo.
- **Um Vídeo por requisição.** Um segundo arquivo no mesmo multipart dá `400`. Vários Vídeos ao mesmo tempo (RF01) são várias requisições, como no YouTube. Uma página que aceite vários arquivos (A12) manda uma requisição por arquivo, e cada envio falha sozinho.
- **Limite de tamanho:** `http.MaxBytesReader` com teto configurável por variável de ambiente. Estourou o teto: `413`, o temporário é apagado e nenhum Vídeo é criado. O timeout de leitura é configurável e vale só nessa rota (`http.ResponseController.SetReadDeadline`). Os timeouts do resto do servidor continuam curtos. Os valores padrão, e os limites de duração e de frames, ficam no A9.
- **Validação na API por conteúdo, não por extensão** (corrige Q04): a API confere os magic bytes do início da parte (MP4/MOV, MKV/WebM, AVI e outros formatos aceitos). Se não reconhecer o formato: `415`, nada é gravado. A validação profunda é do Worker, via `ffprobe`: um arquivo que passa nos magic bytes mas está corrompido vira Falha definitiva com Motivo `video_invalido` (ADR-0004).
- **Resposta:** `202 Accepted`, com `Location: /videos/{id}` e corpo `{id, status}`, depois da ordem do ADR-0003 (arquivo → transação → resposta). O nome original é sanitizado (comprimento máximo, sem caracteres de controle nem separadores de caminho) e guardado só para exibição.

**Download: `GET /videos/{id}/zip`.**

- Servido com `http.ServeContent` sobre `/data/videos/{video_id}/frames.zip`. Isso dá suporte a `Range` (download retomável), a `If-Modified-Since` e a `Content-Disposition: attachment` com o nome `<nome original>-frames.zip`.
- **Checagem de dono antes de tudo** (corrige Q02). Vídeo de outro Usuário e Vídeo inexistente respondem o mesmo `404`, para não revelar que um Vídeo existe.
- Vídeo ainda não concluído: `409`. Vídeo com Falha definitiva: `409`, com o Motivo da falha no corpo.

## Opções consideradas

- **Corpo cru (`Content-Type: video/*`)**: rejeitado. É um pouco mais simples no servidor, mas um formulário HTML comum não envia assim (A12) e o nome original teria de ir num header.
- **`FormFile`/`ParseMultipartForm`**: rejeitado. É o modelo do projeto base: bufferiza até 32 MB em memória por requisição e grava o resto num temporário fora do volume. Com vários envios simultâneos, a memória da API cresce com o tamanho dos vídeos.
- **Vários Vídeos por requisição**: rejeitado. A resposta teria aceite parcial (o primeiro arquivo entra e o segundo falha?). Também não há requisito que peça isso.
- **Upload retomável em pedaços (tus ou protocolo próprio)**: rejeitado. Exige estado de sessão de upload e limpeza de sessões abandonadas, sem requisito que peça. O download retomável sai de graça com `ServeContent`.
- **`ffprobe` na API antes do `202`**: rejeitado. Põe o ffmpeg na imagem da API e CPU dentro da requisição. O Worker já roda o `ffprobe`.
- **Só a extensão**: rejeitado. É o Q04: qualquer arquivo renomeado passa.
- **`403` para Vídeo de outro Usuário**: rejeitado. Confirma que aquele `id` existe.

## Consequências

- A API mantém uma conexão aberta por envio em andamento. A quantidade de envios simultâneos que ela aguenta, e se escala em réplicas, entra no A9.
- O teto de tamanho, a lista de formatos aceitos e o timeout da rota de envio são configuração. Os testes cobrem `413`, `415` e `400` com arquivos pequenos e teto baixo.
- A validação fica em duas camadas (magic bytes na API, `ffprobe` no Worker). Um vídeo corrompido é aceito com `202` e só falha depois. O Usuário vê isso no status e na notificação (A6), não na resposta do envio.
- Rejeições síncronas (`400`, `413`, `415`) não criam Vídeo nem linha na outbox. Só sobra um temporário no volume, se a API cair no meio, e ele entra na retenção.
- Toda mudança nesses handlers passa pelo `/security-review` (regra do `CLAUDE.md`: upload, download, zip).
- Fecha Q02 (dono checado no download), Q04 (validação por conteúdo) e o mecanismo de Q05 (os valores ficam no A9).
