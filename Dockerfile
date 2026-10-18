# syntax=docker/dockerfile:1

# ---- build: compiles the bot and the goose migration tool -------------------
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static binaries (CGO off): they run on the empty distroless images below.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot \
 && CGO_ENABLED=0 GOBIN=/out go install -trimpath -ldflags="-s -w" github.com/pressly/goose/v3/cmd/goose@v3.24.1

# ---- migrate: applies the SQL migrations (docker compose service "migrate") --
FROM gcr.io/distroless/static-debian12:nonroot AS migrate
COPY --from=build /out/goose /goose
COPY migrations /migrations
ENTRYPOINT ["/goose"]

# ---- bot: the default (last) stage ------------------------------------------
# distroless/static has no shell, package manager or curl: only CA certificates
# and the binary. It runs as a non-root user. Time zones are compiled into the
# binary (time/tzdata), so TIMEZONE works without OS files.
FROM gcr.io/distroless/static-debian12:nonroot AS bot
COPY --from=build /out/bot /bot
USER nonroot:nonroot
# 8080: webhook (UPDATES_MODE=webhook); 9090: /metrics, /healthz, /readyz
EXPOSE 8080 9090
# No curl in the image: the binary probes its own /readyz.
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 CMD ["/bot", "healthcheck"]
ENTRYPOINT ["/bot"]
