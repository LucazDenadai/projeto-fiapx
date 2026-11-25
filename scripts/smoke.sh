#!/usr/bin/env bash
# Smoke test da stack que já está no ar (ADR-0012, ADR-0013): roda a coleção do
# Postman inteira com o newman, enviando um vídeo gerado na hora pelo ffmpeg.
#
#   scripts/smoke.sh                        http://localhost:8080
#   BASE_URL=http://outra:8080 scripts/smoke.sh
set -euo pipefail
cd "$(dirname "$0")/.."

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

ffmpeg -hide_banner -loglevel error \
  -f lavfi -i testsrc=duration=5:size=640x360:rate=25 -pix_fmt yuv420p "$dir/video.mp4"

npx --yes newman@6.2.3 run docs/api/fiapx.postman_collection.json \
  --environment docs/api/fiapx.postman_environment.json \
  --env-var "base_url=${BASE_URL:-http://localhost:8080}" \
  --working-dir "$dir" --timeout 300000
