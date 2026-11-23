# Imagem do Worker (ADR-0012): build estático e base mínima com ffmpeg
# (a distroless não tem ffmpeg). Usuário não-root com o mesmo uid da API
# (65532), para os dois gravarem no volume compartilhado.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM alpine:3.23
RUN apk add --no-cache ffmpeg \
 && addgroup -S -g 65532 nonroot \
 && adduser -S -D -H -u 65532 -G nonroot nonroot \
 && mkdir -p /data \
 && chown 65532:65532 /data
COPY --from=build /out/worker /worker
USER 65532:65532
ENTRYPOINT ["/worker"]
