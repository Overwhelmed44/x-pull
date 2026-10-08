# x-pull

One Docker container running Xray and a Go HTTP user poller, managed by
Supervisor installed through apt. Xray's binary and geo assets come from the
official `ghcr.io/xtls/xray-core:26.3.27` image; the runtime is Debian slim.

## Run

Set your endpoint and inbound tags in `configs/poller.example.json`, and
configure your Xray transport in `configs/xray.example.json`:

```sh
docker compose up --build -d
docker compose logs -f
```

Compose runs one service containing both processes. The example publishes
VLESS on host `127.0.0.1:10086`. Xray's API stays inside the container at
`127.0.0.1:10085`. The example transport is plaintext; use your normal
TLS/REALITY settings for public traffic.

The default endpoint points to the Docker host. For a local demo:

```sh
python3 -m http.server 8080 --bind 0.0.0.0 --directory configs
```

Edit `configs/users.example.json` to change users. No container restart is
needed for user changes. Restart the container after changing its config.

## HTTP users

Each configured inbound fetches a complete user list:

```json
{
  "users": [
    {
      "email": "alice@example.com",
      "id": "6ba7b810-9dad-41d1-80b4-00c04fd430c8",
      "flow": "xtls-rprx-vision",
      "level": 0
    }
  ]
}
```

VLESS and VMess use `id`; VMess omits `flow`. Trojan uses `password` instead
of `id`. `level` defaults to zero. Users should have unique emails and
credentials. Unknown JSON fields are ignored. Xray handles account validation;
API errors are logged as returned. HTTP status and JSON decoding errors are
also logged, and retried on the next poll.

The poller adds, updates, and removes users to match the endpoint. It prepends
`email_prefix` (default `pull:`) to source emails so it can leave other users
alone. An empty list removes all users with that prefix. Changes use Xray's
remove/add API and are not transactional. Each poll reads live state, so
users are restored after an Xray restart.

## Configuration

See `configs/poller.example.json`. Defaults:

| Setting | Default |
| --- | --- |
| `poll_interval` | `30s` |
| `xray.address` | `127.0.0.1:10085` |
| `xray.timeout` | `5s` |
| `http.timeout` | `10s` |
| `inbounds[].email_prefix` | `pull:` |

Each inbound specifies `tag`, `protocol` (`vless`, `vmess`, or `trojan`),
`source.url`, and optional `source.headers`. To read a header from the
container environment:

```json
"headers_env": {"Authorization":"XPULL_AUTHORIZATION"}
```

Pass `XPULL_AUTHORIZATION` through the Compose service's `environment`.

## Processes and logs

Supervisor starts and restarts both processes. Docker stop signals are
handled by Supervisor. Its configuration is `deploy/supervisord.conf`.
Both processes log to container stdout/stderr:

```sh
docker compose logs -f
docker compose exec xray supervisorctl status
docker compose exec xray supervisorctl restart xray
```

## Project

```text
cmd/x-pull/          Entry point
internal/poller/    Config, polling/sync, Xray API client
configs/            Three example JSON files
deploy/             Supervisor configuration
tests/              Go tests and container smoke test
Dockerfile
compose.yaml
```

```sh
make build                 # Go 1.26+ required outside Docker
make test
make vet
make docker-build
python3 tests/container.py # uses the built image
```

The poller binary also supports `-config PATH` and `-once` for local use.
