# Imagem da API (ADR-0012): build estático, base distroless, usuário não-root.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian13:nonroot
# /data já pertence ao usuário nonroot, então o volume nomeado nasce gravável.
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/api /api
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/api"]
