# x-pull

A **single Docker container** containing Xray and a Go HTTP user poller.
The Dockerfile extends the official `ghcr.io/xtls/xray-core:26.3.27` image and
adds two static Go binaries: the poller and a PID 1 process supervisor.
The supervisor starts Xray and the poller inside that same container.
The poller retrieves complete user snapshots over HTTP and reconciles them
through Xray's loopback gRPC API. Xray authenticates incoming connections.

Supports VLESS (including Vision), VMess AEAD, and Trojan. Running the image
requires Docker; the build stage supplies Go 1.26.8. The bundled Xray and live
integration tests use **v26.3.27** (`v1.260327.0` Go module).

## Quick start

Edit `configs/poller.docker.example.json`: set `source.url` to your HTTP user
endpoint and match its inbound tag to `configs/xray.docker.example.json`.
Then start the one-service Compose deployment:

```sh
docker compose up --build -d
docker compose logs -f
```

Both Xray and the Go poller run inside the `xray` container. There is one
Compose service. To stop both processes:

```sh
docker compose down
```

For a local demonstration, the default source URL reaches an HTTP server
on the Docker host via `host.docker.internal`. Start this host-side fixture
before Compose, then edit `configs/users.example.json` to see updates:

```sh
python3 -m http.server 8080 --bind 0.0.0.0 --directory configs
```

The fixture is your external user source; it is not a second Xray/poller
container. Linux host access is configured by Compose's `extra_hosts`.
In production, use the URL of your existing control server instead.

The sample publishes VLESS at `127.0.0.1:10086` on the Docker host, with the
in-container listener at `0.0.0.0:10086`. It uses plaintext VLESS for local
testing. Configure TLS/REALITY and your transport for public traffic, and
change the host port binding as needed. Xray's API stays at
`127.0.0.1:10085` **inside the container** and is not published.

Build and run without Compose:

```sh
docker build -t x-pull:local .
docker run -d --name x-pull --restart unless-stopped \
  --stop-timeout 12 \
  --add-host host.docker.internal:host-gateway \
  -p 127.0.0.1:10086:10086 \
  --mount "type=bind,src=$(pwd)/configs/xray.docker.example.json,dst=/etc/x-pull/xray.json,readonly" \
  --mount "type=bind,src=$(pwd)/configs/poller.docker.example.json,dst=/etc/x-pull/poller.json,readonly" \
  x-pull:local
```

The image retains Xray's assets, certificate roots, and nonroot UID 65532.
Bind-mounted configs must be readable by that user (for example, mode `0644`).
HTTPS user sources use
normal certificate verification. Logs from both processes go to container
stdout/stderr; the poller and supervisor use JSON logs.

## Container lifecycle

PID 1 validates both configs, starts Xray, then starts the poller. The poller
retries if the API is still starting. When either child exits unexpectedly,
PID 1 terminates its sibling and exits nonzero so Docker's restart policy
restarts the complete pair. HTTP/API request failures are retried by the
poller and do not stop the container.

`docker stop` sends SIGTERM to PID 1, which forwards SIGTERM to both child
process groups, waits for them, and reaps them. After eight seconds it uses
SIGKILL for children that have not stopped. Normal shutdown exits zero.
Compose gives shutdown twelve seconds. Use a Docker stop timeout longer than
the supervisor's grace period.

The default files inside the image are `/etc/x-pull/xray.json` and
`/etc/x-pull/poller.json`. Mount your own versions there. To use other paths
or change the shutdown grace, pass launcher arguments after the image name:

```sh
docker run ... x-pull:local \
  -xray-config /config/xray.json -poller-config /config/poller.json \
  -shutdown-grace 8s
```

To use a different compatible official Xray version:

```sh
docker build --build-arg XRAY_IMAGE=ghcr.io/xtls/xray-core:26.3.27 -t x-pull:local .
```

Xray must provide `GetInboundUsers` and `AlterInbound`; older releases without
enumeration fail synchronization before making changes. Use Docker Buildx
for other Linux architectures; the builder uses `TARGETOS`/`TARGETARCH`.

## HTTP contract

Each inbound has one endpoint. A GET must return **200 OK** and a complete JSON
snapshot, without pagination. The `users` array is mandatory and cannot be null:

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

| Field | Meaning |
| --- | --- |
| `email` | Required stable user identity, unique after lowercase normalization. It need not be an actual email address. |
| `id` | Required canonical UUID for VLESS and VMess; forbidden for Trojan. |
| `password` | Required nonempty Trojan password; forbidden for VLESS and VMess. |
| `flow` | VLESS only: omitted/empty or `xtls-rprx-vision`. |
| `level` | Optional unsigned 32-bit Xray policy level, defaults to `0`. |

Trojan endpoint example:

```json
{"users":[{"email":"alice@example.com","password":"replace-with-a-secret","level":0}]}
```

VMess uses the same UUID schema as VLESS, with no `flow`. Only modern AEAD
accounts are supported. UUIDs and emails are normalized to lowercase; passwords
are preserved exactly. Duplicate emails or credentials, invalid accounts,
unknown JSON fields, truncated responses, and multiple JSON objects reject
the whole snapshot before any API mutation.

Sources always return a complete snapshot: `204`, `304`, redirects, and other
non-200 responses count as failures. Redirects are refused so authentication
headers cannot be forwarded to another destination. The client does not use
ETags; every successful poll still checks live Xray state.

## Configuration

See [configs/poller.docker.example.json](configs/poller.docker.example.json) for a complete
working example. JSON is strict: unknown keys are errors. Duration values are
strings such as `30s`, `5s`, and `1m`. Configuration is loaded at startup;
restart the container to change it.

| Setting | Default | Meaning |
| --- | --- | --- |
| `poll_interval` | `30s` | Delay after a completed cycle; the first cycle starts immediately. |
| `workers` | `4` | Maximum concurrent inbound syncs, between 1 and 256. |
| `xray.address` | `127.0.0.1:10085` | Xray gRPC host and port. |
| `xray.timeout` | `5s` | Deadline for each API call, including connection establishment. |
| `http.timeout` | `10s` | Total HTTP request timeout, including reading its body. |
| `http.max_response_bytes` | `4194304` | Decompressed response limit, up to 64 MiB; gRPC receive limit is twice this value. |
| `inbounds` | Required | Array of inbound configurations with unique tags. |
| `inbounds[].tag` | Required | Existing Xray inbound tag. |
| `inbounds[].protocol` | Required | `vless`, `vmess`, or `trojan`. |
| `inbounds[].email_prefix` | `pull:` | Ownership prefix, prepended to each source email. |
| `inbounds[].allow_empty` | `false` | Permit an explicit empty snapshot to revoke every managed user. |
| `inbounds[].source.url` | Required | HTTP(S) snapshot URL, without embedded basic-auth credentials. |
| `inbounds[].source.headers` | `{}` | Literal request headers. |
| `inbounds[].source.headers_env` | `{}` | Header name to environment-variable name mapping. |

Add further entries in `inbounds` for additional tags/protocols/endpoints.
Inbound synchronization is independent, with a bounded worker pool and no
overlapping cycles. One failing endpoint does not stop other inbounds.
Reconciliation indexes emails and credentials; local planning takes
O(n log n) time and O(n) memory, plus one RPC per changed account.

For an authenticated endpoint, put the full header value in the environment:

```sh
export XPULL_AUTHORIZATION='Bearer your-token'
```

```json
"source": {
  "url": "https://users.example.com/v1/inbounds/vless-in/users",
  "headers": {"X-Node-ID":"node-1"},
  "headers_env": {"Authorization":"XPULL_AUTHORIZATION"}
}
```

Pass the variable into the container with `docker run -e XPULL_AUTHORIZATION`
or add it to the single Compose service's `environment`:

```yaml
environment:
  XPULL_AUTHORIZATION: ${XPULL_AUTHORIZATION:?set the authorization header}
```

Missing/empty environment variables fail startup. Header names are validated
and cannot occur twice, including case differences or literal/environment
collisions. Credentials, response bodies, URLs, and Xray's raw RPC error
messages are excluded from operational logs; RPC failures report status codes.

## Ownership and failure behavior

Source email `alice@example.com` becomes Xray email `pull:alice@example.com`.
That full email is also used by Xray's email-based statistics. Only users with
the configured prefix are removed or updated. Other enumerated users are
retained, and credential conflicts with those users reject synchronization.
Reserve the prefix exclusively for x-pull; run one writer for each inbound.

For each successful fetch, x-pull validates the snapshot, reads current users,
adds/replaces desired users, then removes stale managed users. Unchanged users
cause no writes. Renaming an email transfers its credential. Advanced VLESS
account settings outside the documented schema are refused on managed users
to avoid erasing settings x-pull cannot represent.

HTTP failures or invalid snapshots make no changes. API enumeration failures
also make no changes. An unexpected empty list is refused unless `allow_empty`
is enabled. Enable that flag when an empty list is a legitimate revocation
signal from your authoritative endpoint.

Xray has no atomic user-update API. Updates/credential transfers use remove
then add, briefly interrupting admission for those users. If adding fails,
x-pull attempts to restore the displaced accounts and stops that inbound's
sync before pruning unrelated stale users. Earlier successful mutations
remain applied. Timeouts have uncertain outcomes, and rollback can fail during
an outage or shutdown; the next successful poll reads live state and converges
again. Operational failures are retried after `poll_interval`.

No credentials or synchronization state are persisted. Xray restarts discard
runtime users; the next successful HTTP fetch and API sync restores them.
If the HTTP source is unavailable after a restart, restoration waits until it
recovers. Existing runtime users remain in place during source outages, so
revocations also wait until a valid snapshot arrives.

Xray's `GetInboundUsers` omits some accounts with empty emails. Give every
static account a nonempty email if you mix static and pulled users; otherwise
credential conflicts with anonymous static accounts cannot be detected. Empty
`clients` arrays on dedicated managed inbounds are the simplest deployment.
Removing an inbound entry from x-pull's config does not clean up its existing
runtime users. To retire one, first serve `{"users":[]}` with `allow_empty:true`.
Changing the ownership prefix leaves users with the old prefix unmanaged.

## Project layout and development

```text
cmd/x-pull/          CLI and dependency wiring
cmd/x-pull-container/ Container PID 1 and startup validation
internal/config/    Strict JSON config, defaults, environment headers
internal/user/      User model, protocol validation, ownership identity
internal/source/    HTTP snapshot client
internal/reconcile/ Live-state diff, credential transfers, rollback
internal/xray/      Official Xray protobuf/gRPC adapter
internal/service/   Polling, worker limits, failure isolation
internal/supervisor/ Child lifecycle, signals, shutdown timeout, reaping
configs/            Working config, endpoint, and Xray examples
scripts/            End-to-end container smoke test
Dockerfile          Build Go binaries and extend the official Xray image
compose.yaml        One service containing Xray and the poller
```

The reconciler depends on a small `Backend` interface and the service depends
on `Source` and `Syncer` interfaces. To support another HTTP schema, implement
a source adapter returning `[]user.User`. To add a protocol, extend validation
in `internal/user` and account conversion in `internal/xray`; scheduling and
reconciliation stay independent of transport details.

```sh
make test       # race detector, unit tests, and a live Xray integration test
make vet
make build check
make docker-build
python3 scripts/test-container.py x-pull:local
```

Tests cover malformed/oversized HTTP responses, authentication headers,
timeouts, cancellation, validation, additions, updates, revocations,
idempotence, ownership, renames, credential swaps, rollback, worker limits,
and failure isolation. The integration test starts actual loopback Xray
inbounds and HandlerService for all three protocols, verifies runtime account
changes, and checks reconnection and restoration after restarting the core. It does not
require an installed Xray binary or an external HTTP service.

Supervisor tests use actual child processes to verify signal forwarding,
sibling shutdown after failure, SIGKILL fallback, and reaping. The Docker
smoke test verifies both processes share one container, HTTP-driven user
add/update/remove, clean `docker stop`, and invalid-config failures. CI runs
both the Go test suite and the container build/smoke test.

For local Go development (Go 1.26+), `bin/x-pull -config
configs/config.example.json` runs only the poller. `-once` synchronizes once
and exits nonzero on failure; `-check` validates configuration without network
calls. The production image always starts both processes through PID 1.
