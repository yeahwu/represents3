FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/represents3 .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app \
    && adduser -S -G app app

WORKDIR /app

COPY --from=builder /out/represents3 /app/represents3
RUN mkdir -p /app/cert-cache && chown -R app:app /app

USER app

ENV CERT_CACHE_DIR=/app/cert-cache
ENV LISTEN_HTTP=:8080
ENV LISTEN_HTTPS=:8443

EXPOSE 8080 8443
VOLUME ["/app/cert-cache"]

ENTRYPOINT ["/app/represents3"]
