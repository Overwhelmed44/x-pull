# x-pull

A Go companion service that periodically pulls users from HTTP endpoints and
reconciles them into a running Xray core through its gRPC `HandlerService`.
Xray continues to authenticate connections; x-pull maintains its runtime users.
Run the two processes alongside one another, or use the supplied systemd unit.

Supports VLESS (including Vision), VMess AEAD, and Trojan. Requires Go 1.26+
and an Xray release providing `GetInboundUsers` and `AlterInbound`. The live
integration test uses Xray **v26.3.27** (`v1.260327.0` Go module). Older releases
without user enumeration are unsupported and fail before making changes.

## Quick start

Build and validate the example configuration:

```sh
make build
./bin/x-pull -config configs/config.example.json -check
```

Start Xray in one terminal:

```sh
xray run -config configs/xray.example.json
```

Serve the example user endpoint in another:

```sh
python3 -m http.server 8080 --bind 127.0.0.1 --directory configs
```

Start the companion service:

```sh
./bin/x-pull -config configs/config.example.json
```

Edit `configs/users.example.json` to change the desired users. The next poll
applies the change without restarting Xray. For a single synchronization,
pass `-once`; it exits nonzero if any inbound fails. `-check` validates config
and resolves environment headers without making network calls. Logs are JSON
on stderr. SIGINT/SIGTERM cancel in-flight requests and stop the service.

The Xray example exposes a plaintext VLESS listener on **loopback only** for
local testing. For public traffic, configure your existing inbound's TLS/REALITY
and transport normally; x-pull changes users, not inbound transport settings.
Enable `HandlerService` and set `api.listen` to `127.0.0.1:10085` in your actual
Xray config. The inbound tag must match x-pull's configured tag. The API uses
plaintext gRPC with no authentication; keep it on loopback or a trusted private
link. HTTP sources support normal HTTPS certificate verification.

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

See [configs/config.example.json](configs/config.example.json) for a complete
working example. JSON is strict: unknown keys are errors. Duration values are
strings such as `30s`, `5s`, and `1m`. Configuration is loaded at startup;
restart x-pull to change it.

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

## Run with systemd

```sh
sudo install -m 0755 bin/x-pull /usr/local/bin/x-pull
sudo install -d -m 0755 /etc/x-pull
sudo install -m 0644 configs/config.example.json /etc/x-pull/config.json
sudo install -m 0644 deploy/x-pull.service /etc/systemd/system/x-pull.service
```

Edit `/etc/x-pull/config.json` for your endpoint and inbound tags. If using
environment headers, create `/etc/x-pull/environment` with mode `0600`, owned
by root, containing `XPULL_AUTHORIZATION="Bearer your-token"`. systemd reads
this file before starting the unprivileged service. Keep secrets in that file;
the config must be readable by the unit's dynamic user.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now x-pull
journalctl -u x-pull -f
```

The unit starts after `xray.service` and remains running through Xray restarts
so it can reconnect and repopulate users. Adjust the unit name if your Xray
installation uses a different one.

## Project layout and development

```text
cmd/x-pull/          CLI and dependency wiring
internal/config/    Strict JSON config, defaults, environment headers
internal/user/      User model, protocol validation, ownership identity
internal/source/    HTTP snapshot client
internal/reconcile/ Live-state diff, credential transfers, rollback
internal/xray/      Official Xray protobuf/gRPC adapter
internal/service/   Polling, worker limits, failure isolation
configs/            Working config, endpoint, and Xray examples
deploy/             Hardened systemd unit
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
```

Tests cover malformed/oversized HTTP responses, authentication headers,
timeouts, cancellation, validation, additions, updates, revocations,
idempotence, ownership, renames, credential swaps, rollback, worker limits,
and failure isolation. The integration test starts actual loopback Xray
inbounds and HandlerService for all three protocols, verifies runtime account
changes, and checks reconnection and restoration after restarting the core. It does not
require an installed Xray binary or an external HTTP service.
