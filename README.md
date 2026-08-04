# HttpHop

Expose internal HTTP services to the public internet through a lightweight server with a long-polling tunnel — no port forwarding or nginx required.

HttpHop runs a **Server** on a machine with a public IP and domain, and a **Client** on an internal network. Public HTTPS requests are routed by host (and optionally path prefix), forwarded through a [pollmux](https://github.com/DiamondGo/pollmux) virtual connection, and served by your local HTTP backend.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full design and [plans/IMPLEMENTATION.md](plans/IMPLEMENTATION.md) for the build plan.

Example configs for a full deployment: [configs/server.builderrors.example.yaml](configs/server.builderrors.example.yaml), [configs/client.builderrors.example.yaml](configs/client.builderrors.example.yaml).

## How it works

```
  Internet user                Public VPS (HttpHop Server)          Internal network (HttpHop Client)
  ─────────────                ───────────────────────────          ────────────────────────────────

  https://myapp.example.com    :443  TLS + route by Host
        │                      tunnel.example.com  ←── long poll ──►  Client (outbound only)
        │                            │                                  │
        └──── HTTP request ─────────►│── yamux stream ─────────────────►│──► 127.0.0.1:8080
                                     │                                  local HTTP service
```

- The **Server** sits on a machine with a public IP and your domain. It terminates HTTPS for users and maintains tunnels from Clients.
- The **Client** runs where your service lives. It **initiates** outbound connections to the Server — no port forwarding on the internal network.
- One Client instance exposes **one** local `target` (e.g. `127.0.0.1:8080`). Run multiple Clients for multiple services.

---

## Usage example: internal service on a public VPS

This walkthrough exposes an app on `127.0.0.1:8080` inside a home lab as **`https://myapp.builderrors.com`**.

### Roles

| Machine | Runs | Needs |
|---|---|---|
| **VPS** (203.0.113.10) | `httphop-server` | Public IP, domain `builderrors.com`, ports 80/443 open |
| **Home lab** | Your app + `httphop-client` | Outbound HTTPS to `tunnel.builderrors.com`, app on `127.0.0.1:8080` |

### 1. Build binaries

On both machines (or cross-compile once and copy):

```bash
git clone https://github.com/DiamondGo/HttpHop.git
cd HttpHop
make build
# → bin/httphop-server, bin/httphop-client
```

### 2. DNS (Cloudflare)

| Type | Name | Content | Proxy |
|---|---|---|---|
| A | `@` | 203.0.113.10 | Proxied (optional) |
| A | `myapp` | 203.0.113.10 | Proxied (optional) |
| A | `tunnel` | 203.0.113.10 | **DNS only (grey cloud)** recommended |

- **`myapp.builderrors.com`**: where users visit your app.
- **`tunnel.builderrors.com`**: control plane — Client connects here. Grey cloud avoids Cloudflare cutting long polls.

### 3. Server config (VPS)

Copy [configs/server.builderrors.example.yaml](configs/server.builderrors.example.yaml) to `/etc/httphop/server.yaml`. Generate a random token (≥ 32 characters):

```bash
openssl rand -hex 32
```

Set that token in `tunnels[0].token`. Ensure `tls.disable: false` for production (Let's Encrypt via autocert on ports 80/443).

Start the Server:

```bash
sudo mkdir -p /var/lib/httphop/certs
./bin/httphop-server -config /etc/httphop/server.yaml
```

Firewall: allow **80** (ACME + redirect) and **443** (HTTPS).

### 4. Client config (home lab)

Copy [configs/client.builderrors.example.yaml](configs/client.builderrors.example.yaml) to `/etc/httphop/client.yaml`.

- **`server.url`**: `https://tunnel.builderrors.com` (control host, not `myapp`)
- **`server.token`**: same token as on the Server
- **`local.target`**: where your app listens, e.g. `127.0.0.1:8080`
- **`client_id`**: stable name (e.g. `home-lab-01`); keep the same across restarts

Start your app, then the Client:

```bash
./bin/httphop-client -config /etc/httphop/client.yaml
```

The Client registers the tunnel; no inbound ports required on the home network.

### 5. Verify

On the VPS (tunnel registered):

```bash
curl -s http://127.0.0.1:9090/status | jq .
# → "subdomain": "myapp", "local_health": "ok"
```

From anywhere:

```bash
curl -i https://myapp.builderrors.com/your-api-path
```

Your app receives the request at `/your-api-path` with headers including:

- `Host: myapp.builderrors.com`
- `X-Forwarded-For: <real client IP>`
- `X-Forwarded-Proto: https`

Use `X-Forwarded-For` (not `RemoteAddr`) for client IP — TCP to the app always comes from the local Client.

### 6. Run under systemd (optional)

**Server** (`/etc/systemd/system/httphop-server.service`):

```ini
[Unit]
Description=HttpHop Server
After=network-online.target

[Service]
ExecStart=/usr/local/bin/httphop-server -config /etc/httphop/server.yaml
Restart=on-failure
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

**Client** — same pattern with `httphop-client`. Start Client after your app service.

---

## Usage example B: apex path (optional)

Use only if you need `https://builderrors.com/service/*` instead of a subdomain.

**Server** — add or replace tunnel binding:

```yaml
tunnels:
  - subdomain: "@"
    path_prefix: "/service"
    strip_prefix: true
    token: "..."
    max_clients: 1
```

**Client** — unchanged (`local.target: "127.0.0.1:8080"`). HttpHop strips `/service` before forwarding and rewrites `Location` / `Set-Cookie Path` on responses.

**DNS** — only `builderrors.com` → VPS; no extra record for the path.

**Caveat** — front-end apps may still need `PUBLIC_URL=https://builderrors.com/service`. Prefer Example A when you can.

---

## Quick start (local dev)

```bash
make build
./bin/httphop-server -config configs/server.example.yaml
./bin/httphop-client -config configs/client.example.yaml
```

The bundled examples use `tls.disable: true` and `dev_listen: :8443` for local testing without certificates.

---

**Recommended:** map a whole subdomain to one internal service. Public and backend paths stay identical — redirects, cookies, and static assets work without extra configuration.

```
https://myapp.example.com/auth  →  Client  →  127.0.0.1:8080/auth
```

Server (`configs/server.example.yaml`):

```yaml
root_domain: example.com
control_host: tunnel.example.com

tunnels:
  - subdomain: "myapp"          # myapp.example.com
    token: "..."
    max_clients: 1
```

Client: point `local.target` at your service. No path settings on the Client.

DNS: add `myapp.example.com` (A/AAAA or CNAME) to your Server, same as any other public host.

---

## Alternative: apex path prefix (use when you must)

If you need `https://example.com/service/*` instead of a subdomain, HttpHop supports **path prefix + strip** on the apex host (`subdomain: "@"`):

```
https://example.com/service/auth  →  127.0.0.1:8080/auth
```

Server:

```yaml
tunnels:
  - subdomain: "@"
    path_prefix: "/service"
    strip_prefix: true
    token: "..."
```

For this mode, HttpHop rewrites **response headers** on the way out (similar to nginx `proxy_redirect` / `proxy_cookie_path`):

- `Location: /login` → `/service/login`
- `Set-Cookie; Path=/` → `Path=/service`

That covers most simple redirects and session cookies. It does **not** rewrite HTML/JS bodies (no `sub_filter`). Static assets and front-end routers may still need the app configured with a public base path (e.g. `PUBLIC_URL=https://example.com/service`).

**Use subdomain mapping unless you have a strong reason to share the apex domain.**

---

## Example: builderrors.com

| Goal | Server binding | User visits |
|---|---|---|
| **Recommended** | `subdomain: "myapp"` | `https://myapp.builderrors.com/...` |
| Apex path (optional) | `subdomain: "@"`, `path_prefix: "/service"`, `strip_prefix: true` | `https://builderrors.com/service/...` |

Control plane (Client connects here): `https://tunnel.builderrors.com` — use **DNS only (grey cloud)** on Cloudflare if possible, so long polls are not cut.

---

## Known limitations

1. **Asymmetric throughput**: downstream is capped at roughly `poll_buffer_size / RTT` (~1.7 MB/s at 256 KB buffer and 150 ms RTT). Upstream is higher because sends batch adaptively.
2. **Single-level subdomains only**: `a.b.example.com` is not supported; apex `example.com` uses `subdomain: "@"`.
3. **Path routing**: prefix match + optional strip only — no regex or header-based rules. Prefer subdomains over path prefixes when you can.
4. **One local target per Client**: expose multiple backends with multiple Client instances or app-level routing.
5. **CDN/proxy timeouts**: long polls may be cut by upstream proxies; tune `poll_timeout` accordingly.
