# Imagem do Notifier (ADR-0012): build estático, base distroless, usuário não-root.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/notifier ./cmd/notifier

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/notifier /notifier
USER 65532:65532
ENTRYPOINT ["/notifier"]
