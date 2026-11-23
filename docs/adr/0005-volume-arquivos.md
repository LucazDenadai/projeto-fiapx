---
status: accepted
resolve: RT01, RT02, A4
---

# ADR-0005 — Volume nomeado do Docker para vídeos e zips

## Contexto

No projeto base, vídeos, frames e zips ficam em pastas locais (`uploads/`, `temp/`, `outputs/`). Essas pastas são servidas como arquivos estáticos públicos (Q01, `legacy/main.go:48-49`), e o nome de cada arquivo vem do nome enviado pelo cliente (Q03). Com API e Workers em réplicas separadas ([ADR-0001](0001-api-worker-notifier.md)), todos precisam enxergar os mesmos arquivos (RT02), e esses arquivos precisam sobreviver à queda de qualquer container (RT01).

O enunciado não pede object storage: não está na stack recomendada nem nos entregáveis. A plataforma é Docker Compose em um único host (matriz §6).

## Decisão

**Um volume nomeado do Docker (`arquivos`), montado em `/data` na API e nos Workers.** O Notifier não monta o volume. O volume sobrevive à recriação dos containers, e todas as réplicas do mesmo host o compartilham.

- **Caminhos gerados pelo servidor**, nunca a partir do nome enviado pelo cliente (corrige Q03):
  - `/data/videos/{video_id}/original`
  - `/data/videos/{video_id}/frames.zip`

  `video_id` é um UUID gerado pela API e validado antes de montar o caminho, o que impede path traversal. Tudo de um Vídeo fica num único diretório, que a retenção apaga de uma vez. O nome original fica só no Postgres, como metadado de exibição e para o `Content-Disposition`.
- **Gravação atômica:** cada arquivo é gravado num temporário no mesmo volume e depois publicado com `os.Rename`. Um leitor nunca vê um arquivo pela metade. Se a API cair no meio do upload, ou um Worker reentregue cair no meio do zip, sobra só um temporário, nunca um `original` ou `frames.zip` corrompido.
- **Nada servido como estático** (corrige Q01): o download passa por um handler da API que checa o dono (ADR-0001).
- **Frames durante a extração:** ficam num diretório temporário por Processamento (`os.MkdirTemp`), no disco efêmero do container do Worker. O Worker lê o `original` direto do volume, o ffmpeg grava os frames no temporário, e o zip é montado e publicado no volume. O temporário é removido com `defer`. Ao subir, o Worker limpa seu diretório de trabalho, que pode ter sobras de uma queda. Frame não é estado: a reentrega refaz tudo do zero.
- **Código atrás de uma interface:** API e Worker declaram, cada um no seu pacote, a interface de armazenamento de que precisam. Uma única implementação de sistema de arquivos atende às duas. Trocar por object storage depois é escrever outra implementação, sem mudar os chamadores.
- **Recurso no Compose (E02):** o volume é declarado no `docker-compose.yml`, sem script de init. A imagem cria `/data` com dono igual ao usuário não-root da aplicação. Assim o Docker copia essa permissão para o volume novo.

**Fora deste ADR:** se o upload é multipart pela API ou outro meio (A11); por quanto tempo cada arquivo vive (ticket de retenção); limites de tamanho e de duração (A9).

## Opções consideradas

- **Object storage S3-compatível (SeaweedFS, Garage)**: rejeitado. Não é pedido no enunciado e custa um container a mais, um init para criar o bucket, credenciais e ajustes do SDK com servidores não-AWS. Os ganhos são URL pré-assinada e vários hosts, e nenhum dos dois é necessário num Compose de um host. Continua possível como evolução, via outra implementação da interface. O MinIO Community, citado na matriz, foi arquivado em 2026 e não tem mais imagem oficial.
- **Postgres (`bytea` ou large objects)**: rejeitado. Vídeos incham o banco, o backup e a memória das consultas, e o Worker ainda precisaria de um arquivo em disco para o ffmpeg.
- **Bind mount de pasta do host**: rejeitado. Depende do caminho e das permissões do host (Windows/WSL) e tira do Compose a declaração do recurso.
- **Disco local de cada container**: rejeitado. É o `uploads/` do projeto base: a réplica que recebe o upload não é a que processa, e tudo some ao recriar o container.

## Consequências

- Escala horizontal (`--scale worker=N`) funciona dentro de um host. Vários hosts exigiriam trocar a implementação por object storage. Essa troca é aceita como fora de escopo, coerente com a matriz §6.
- Sem URL pré-assinada, os bytes de upload e de download passam pela API. Isso estreita o A11.
- Os testes de armazenamento usam `t.TempDir()`, sem container. O teste de integração do Compose cobre o compartilhamento entre API e Worker.
- O Worker lê o vídeo do volume sem download. Ainda precisa de disco efêmero para frames e zip de um Processamento por unidade de concorrência (`prefetch`). O dimensionamento fica no A9.
- A ordem do ADR-0003 (armazenamento → transação → `202`) grava em `/data/videos/{video_id}/original`, então a API gera o `video_id` antes de gravar.
- O ticket de retenção apaga `/data/videos/{video_id}/` e os temporários órfãos.
- Backup do volume fica fora do escopo.
- Fecha Q01 (nada servido como estático público) e Q03 (caminho não vem do cliente).
