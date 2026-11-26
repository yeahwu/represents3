# represents3

A Go-based HTTPS forward proxy with support for:

- HTTPS proxy endpoint
- `CONNECT` tunnel forwarding
- Standard HTTP request forwarding
- Basic proxy authentication (`Proxy-Authorization`)
- Automatic Let's Encrypt certificates (`autocert`)
- ACME staging mode
- Structured logging (configurable level/format)
- Graceful shutdown

## Environment Variables

Copy the example configuration:

```bash
cp .env.example .env
```

Key variables:

- `PROXY_DOMAINS`: required, certificate domain(s), comma-separated
- `PROXY_USER` / `PROXY_PASS`: recommended, proxy auth credentials
- `ACME_EMAIL`: email used for certificate registration
- `ACME_STAGING`: set to `true` to use Let's Encrypt staging
- `CERT_CACHE_DIR`: certificate cache directory
- `LISTEN_HTTP`: HTTP listen address (ACME challenge)
- `LISTEN_HTTPS`: HTTPS listen address (proxy endpoint)
- `UPSTREAM_DIAL_TIMEOUT`: upstream dial timeout
- `LOG_LEVEL`: log level (`debug|info|warn|error`)
- `LOG_FORMAT`: log format (`text|json`)
- `LOG_SOURCE`: include source location in logs (`true|false`)

## Run Locally

```bash
make tidy
make build
./represents3
```

Or directly:

```bash
go mod tidy
go run .
```

Or Variable

```bash
env PROXY_DOMAINS=proxy.example.com PROXY_USER=proxyuser PROXY_PASS='your-strong-password' ACME_EMAIL=admin@example.com LOG_LEVEL=info go run .
```

## Run with Docker

```bash
cp .env.example .env
docker compose up -d --build
```

Inside the container, the default ports are `8080/8443`, mapped to host `80/443` via Compose.

## Deploy with systemd

Build and install first:

```bash
make build
sudo install -d -m 0755 /var/lib/represents3
make service-install
make service-enable
make service-start
```

Optional: edit `/etc/represents3/env`, then restart the service.

## Client Example

```bash
curl -x https://proxyuser:change-me-strong-password@proxy.example.com:443 https://example.com -v
```
