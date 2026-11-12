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
