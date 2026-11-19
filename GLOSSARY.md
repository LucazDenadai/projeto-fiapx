# FIAP X — Processamento de vídeos

Usuários enviam vídeos; o sistema extrai os frames e devolve um zip para download.

## Linguagem

**Usuário**:
Pessoa autenticada que envia Vídeos e acompanha o status deles.
_Evitar_: cliente, conta

**Vídeo**:
Arquivo enviado por um Usuário, junto com o status do seu Processamento.
_Evitar_: upload, arquivo, mídia

**Processamento**:
Extração dos frames de um Vídeo e seu empacotamento em um Zip de frames.
_Evitar_: conversão, job

**Zip de frames**:
Arquivo compactado com os frames de um Vídeo, que o Usuário baixa.
_Evitar_: output, resultado, pacote

**Falha definitiva**:
Fim de um Processamento sem Zip de frames, com o Vídeo marcado como falho e o Usuário notificado. Não há nova tentativa automática.
_Evitar_: erro, crash

**Motivo da falha**:
Código público que explica ao Usuário por que um Vídeo teve Falha definitiva (`video_invalido`, `sem_frames`, `tempo_excedido`, `erro_interno`). Nunca contém detalhe interno.
_Evitar_: mensagem de erro, stack
