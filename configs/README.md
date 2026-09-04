# HttpHop configuration

Example configs live under `examples/`. **Do not edit examples in place** — copy to `local/` and `secrets/`.

## Layout

```
configs/
  README.md
  examples/
    local/
      server.yaml.example
      client.yaml.example
    builderrors/
      server.yaml.example
      client.yaml.example
    ai.builderrors/       # ai.builderrors.com (single-service VPS)
      server.yaml.example
      client.yaml.example
    secrets/              # token examples (one file per client_id)
      home-gpu-01.token.example
      myai.token.example
  local/                  # your yaml configs (gitignored)
    server.yaml
    client.yaml
  secrets/                # real tokens only (gitignored)
    home-gpu-01.token
    myai.token
```

YAML configs stay in `local/`; **all client tokens live in `secrets/`**, not beside the yaml files.

Each `client_id` has its own token file under `secrets/`.

## Quick start (local dev)

```bash
make config-local
openssl rand -hex 32 > configs/secrets/home-gpu-01.token
chmod 600 configs/secrets/home-gpu-01.token

make build
./bin/httphop-server -config configs/local/server.yaml
./bin/httphop-client -config configs/local/client.yaml
```

## Copy builderrors example

```bash
make config-local
cp configs/examples/builderrors/server.yaml.example configs/local/server.yaml
cp configs/examples/builderrors/client.yaml.example configs/local/client.yaml
cp configs/examples/secrets/myai.token.example configs/secrets/myai.token
openssl rand -hex 32 > configs/secrets/myai.token
chmod 600 configs/secrets/myai.token
```

Production layout on VPS / home lab (same idea):

```
/etc/httphop/
  server.yaml
  client.yaml
  secrets/
    myai.token
```

## Token paths in yaml

`token_file` is relative to the yaml file, e.g. from `configs/local/server.yaml`:

```yaml
token_file: "../secrets/myai.token"
```

Server `clients[].token_file` and Client `server.token_file` (or `services[].token_file` in multi-service format) for the same `client_id` must point to the **same** file content.

Rotate: replace the `.token` file only; `client_id` and routing unchanged.

## Multi-service client

A single client process can tunnel multiple local services. Use a `services` list instead of top-level `client_id`/`local`/`server`:

```yaml
services:
  - client_id: "app"
    token_file: "../secrets/app.token"
    local:
      target: "127.0.0.1:8080"
    server:
      url: "https://app.example.com"
      control_path: "/tunnel"
  - client_id: "blog"
    token_file: "../secrets/blog.token"
    local:
      target: "127.0.0.1:3000"
    server:
      url: "https://blog.example.com"
      control_path: "/tunnel"
```

Top-level `transport`, `health`, and `logging` are shared defaults. Each service sets its own `server` and `token_file`. Per-service `health` overrides are also supported.

The old single-service format (top-level `client_id` + `local` + `server`) is still supported.

## Upgrade existing configs: resumable tunnels

HttpHop now enables pollmux session resume by default. Resume keeps the same
pollmux/yamux session—and therefore active HTTP requests—alive across a short
transport disconnect. It is negotiated only when the tunnel uses WebSocket or
stream mode in **both** directions; batch polling remains compatible but is not
resumable.

Recommended server settings (especially behind Cloudflare/nginx):

```yaml
tunnel:
  enable_websocket: true
  enable_resume: true
  resume_grace: 30s
  max_replay_bytes: 16777216       # 16 MiB per direction per tunnel
  max_detached_resumable: 1024     # lower on small servers; negative disables the cap
```

Recommended client settings:

```yaml
transport:
  prefer_websocket: true
  prefer_resume: true
  max_replay_bytes: 16777216
```

Restart both server and client after editing. Rolling upgrades are safe because
resume negotiation is additive: upgrade the server first, then clients. A new
binary with an old config uses the resume defaults, but resume becomes effective
only after WebSocket or two-way stream is enabled. To retain the old behavior,
set `enable_resume: false` on the server or `prefer_resume: false` on clients.

Memory planning: `max_replay_bytes` is a per-direction ceiling on each side, not
a global pool. Detached resumable sessions also remain allocated for
`resume_grace`, bounded server-side by `max_detached_resumable`. Use a negative
value to disable this cap, or smaller positive values where many tunnels share
a memory-constrained host. Keep proxy
`response_header_timeout` longer than `resume_grace` if requests should wait for
recovery. The status endpoint reports `resumable: true` for a successfully
negotiated tunnel and includes `resume_deadline` while its transport is detached.

When nginx fronts HttpHop, `/tunnel/{id}/resume` must be routed through the same
location and authorization boundary as `/connect`, `/poll`, `/ws`, and DELETE.
For WebSocket, retain HTTP/1.1 plus the `Upgrade` and `Connection` headers shown
in the production example.
