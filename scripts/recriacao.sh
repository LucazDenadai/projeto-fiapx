#!/usr/bin/env bash
# Vídeo aceito não se perde quando o container do RabbitMQ é recriado (RF02,
# ADR-0002): com os Workers parados, envia um Vídeo, recria o RabbitMQ e o
# Vídeo tem de terminar concluido. Mexe na stack que já está no ar.
#
#   scripts/recriacao.sh                        http://localhost:8080
#   BASE_URL=http://outra:8080 scripts/recriacao.sh
set -euo pipefail
cd "$(dirname "$0")/.."

base=${BASE_URL:-http://localhost:8080}
credencial='{"email":"recriacao.'$(date +%s)'@exemplo.com","senha":"Exemplo#123"}'

# campo lê um campo de texto do JSON da entrada.
campo() { sed -n 's/.*"'"$1"'":"\([^"]*\)".*/\1/p'; }

curl -fsS -o /dev/null -X POST "$base/usuarios" -d "$credencial"
autorizacao="Authorization: Bearer $(curl -fsS -X POST "$base/login" -d "$credencial" | campo token)"

docker compose stop worker
id=$(curl -fsS -H "$autorizacao" -F video=@docs/api/video.mp4 "$base/videos" | campo id)
echo "Vídeo $id aceito; esperando o relay publicar"
for _ in $(seq 30); do
  fila=$(docker compose exec -T rabbitmq rabbitmqctl -q list_queues name messages | awk '$1 == "processamento" {print $2}')
  [ "${fila:-0}" -gt 0 ] && break
  sleep 1
done
[ "${fila:-0}" -gt 0 ] || { echo "a mensagem não chegou à fila"; exit 1; }

docker compose up -d --no-build --no-deps --force-recreate --wait rabbitmq
docker compose up -d --no-build --no-recreate worker

for _ in $(seq 45); do
  status=$(curl -fsS -H "$autorizacao" "$base/videos/$id" | campo status)
  [ "$status" = concluido ] && { echo "concluido depois da recriação"; exit 0; }
  sleep 2
done
echo "o Vídeo $id ficou em $status depois da recriação do RabbitMQ"
exit 1
