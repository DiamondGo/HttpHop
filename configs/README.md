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

Server `clients[].token_file` and Client `server.token_file` for the same `client_id` must point to the **same** file content.

Rotate: replace the `.token` file only; `client_id` and routing unchanged.
