# syntax=docker/dockerfile:1
ARG GO_IMAGE=golang:1.26.8-bookworm
ARG XRAY_IMAGE=ghcr.io/xtls/xray-core:26.3.27

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct
COPY go.mod go.sum ./
# Optional CA bundle supports builds behind an HTTPS inspection proxy.
RUN --mount=type=secret,id=ca_bundle \
    if [ -f /run/secrets/ca_bundle ]; then \
      export SSL_CERT_FILE=/run/secrets/ca_bundle GIT_SSL_CAINFO=/run/secrets/ca_bundle; \
    fi; \
    go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/x-pull ./cmd/x-pull && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/x-pull-container ./cmd/x-pull-container
RUN install -d -m 0755 /out/empty

# Extend the official Xray image, retaining its binary, assets, and nonroot user.
FROM ${XRAY_IMAGE}
COPY --from=build --chmod=0755 /out/x-pull /out/x-pull-container /usr/local/bin/
COPY --from=build --chmod=0755 /out/empty /etc/x-pull/
COPY --chmod=0644 configs/poller.docker.example.json /etc/x-pull/poller.json
COPY --chmod=0644 configs/xray.docker.example.json /etc/x-pull/xray.json
EXPOSE 10086/tcp
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/x-pull-container"]
CMD ["-xray-config", "/etc/x-pull/xray.json", "-poller-config", "/etc/x-pull/poller.json"]
