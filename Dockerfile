# syntax=docker/dockerfile:1
ARG XRAY_IMAGE=ghcr.io/xtls/xray-core:26.3.27
FROM ${XRAY_IMAGE} AS xray

FROM golang:1.26.8-bookworm AS build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
COPY go.mod go.sum ./
RUN --mount=type=secret,id=ca_bundle \
    if [ -f /run/secrets/ca_bundle ]; then \
      export SSL_CERT_FILE=/run/secrets/ca_bundle GIT_SSL_CAINFO=/run/secrets/ca_bundle; \
    fi; \
    go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /x-pull ./cmd/x-pull

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends supervisor ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=xray /usr/local/bin/xray /usr/local/bin/xray
COPY --from=xray /usr/local/share/xray /usr/local/share/xray
COPY --from=build /x-pull /usr/local/bin/x-pull
COPY deploy/supervisord.conf /etc/supervisor/supervisord.conf
COPY configs/poller.example.json /etc/x-pull/poller.json
COPY configs/xray.example.json /etc/x-pull/xray.json
EXPOSE 10086/tcp
CMD ["/usr/bin/supervisord", "-c", "/etc/supervisor/supervisord.conf"]
