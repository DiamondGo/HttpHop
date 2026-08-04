# HttpHop 实现指南

> **版本**:与 [ARCHITECTURE.md](../ARCHITECTURE.md) v6 对齐  
> **状态**:待实现  
> **前置依赖**:[pollmux](https://github.com/DiamondGo/pollmux) 库已完成(`go test -race ./...` 全绿);开发期在 `go.mod` 使用 `replace github.com/DiamondGo/pollmux => ../pollmux`

本文是**可执行的实现清单**:按阶段、按步骤列出要创建的文件、要实现的行为、要写的测试与验收命令。**设计理由与结构体签名见 ARCHITECTURE.md**,此处不重复。

---

## 目录

1. [实现总览](#一实现总览)
2. [阶段 0:项目骨架](#阶段-0项目骨架)
3. [阶段 1:配置层](#阶段-1配置层)
4. [阶段 2:注册表与路由](#阶段-2注册表与路由)
5. [阶段 3:服务端控制面](#阶段-3服务端控制面)
6. [阶段 4:服务端公网侧](#阶段-4服务端公网侧)
7. [阶段 5:客户端](#阶段-5客户端)
8. [阶段 6:入口与 TLS](#阶段-6入口与-tls)
9. [阶段 7:集成测试与交付](#阶段-7集成测试与交付)
10. [附录 A:连接模型速查](#附录-a连接模型速查)
11. [附录 B:阶段依赖图](#附录-b阶段依赖图)

---

## 一、实现总览

### 1.1 要交付的二进制

| 二进制 | 入口 | 职责 |
|---|---|---|
| `httphop-server` | `cmd/server/main.go` | 公网 HTTPS + 控制面 + pollmux handler + 路由 + ReverseProxy |
| `httphop-client` | `cmd/client/main.go` | pollmux 隧道 + yamux Accept + 转发本地 HTTP |

### 1.2 不在本仓库实现的范围

- HTTP 长轮询虚拟连接、connect/poll/delete、yamux 配置、ReconnectLoop → **pollmux**
- nginx/Caddy 路径路由 → **明确不做**

### 1.3 全局实现约束(checklist)

实现任意阶段时对照:

- [ ] yamux 只通过 `pollmux.ClientSession` / `pollmux.ServerSession` 创建
- [ ] 路径剥前缀只在 Server `ReverseProxy.Rewrite` 做,Client 不感知公网 `path_prefix`
- [ ] token → host key + pool 在服务端配置写死,Client 不能自选子域名
- [ ] 每条 yamux 流一个 HTTP 请求(`DisableKeepAlives: true`)
- [ ] pollmux 日志边界:HttpHop 用 zap,传给 pollmux 时用 `zapslog.NewHandler`

---

## 阶段 0:项目骨架

**目标**:空项目能编译,目录与工具链就绪。

### 步骤 0.1 — 初始化 Go 模块

**任务**:

1. `go mod init github.com/DiamondGo/HttpHop`
2. `go get` 依赖(版本以当时 latest 为准):
   - `github.com/DiamondGo/pollmux`
   - `github.com/gorilla/mux`
   - `github.com/spf13/viper`
   - `github.com/hashicorp/yamux`(间接依赖,显式 pin 可选)
   - `go.uber.org/zap`
   - `go.uber.org/zap/exp`
   - `golang.org/x/crypto`
3. 若本地开发 pollmux,在 `go.mod` 添加:
   ```go
   replace github.com/DiamondGo/pollmux => ../pollmux
   ```

**产出**: `go.mod`, `go.sum`

### 步骤 0.2 — 目录骨架

**任务**:创建以下空包(每个包至少一个 `doc.go` 或占位 `.go`,保证 `go build ./...` 通过):

```
cmd/server/main.go          // 仅 main(){}
cmd/client/main.go
internal/config/
internal/registry/
internal/router/
internal/server/
internal/client/
configs/
test/
plans/IMPLEMENTATION.md     // 本文
```

**产出**: 目录树与占位 main

### 步骤 0.3 — 工程文件

**任务**:

1. `.gitignore`:Go 默认 + `/bin/` + `configs/*.local.yaml`
2. `Makefile` 目标:
   - `build` → `go build -o bin/httphop-server ./cmd/server` 与 `client`
   - `test` → `go test -race ./...`
   - `lint` → `go vet ./...`(或 staticcheck,可选)
3. `README.md` 占位:一句话定位 + 指向 ARCHITECTURE.md + §18 已知限制摘要

**产出**: `.gitignore`, `Makefile`, `README.md`

### 步骤 0.4 — 示例配置占位

**任务**:复制 ARCHITECTURE.md §10.2 / §10.3 内容到:

- `configs/server.example.yaml`
- `configs/client.example.yaml`

(内容可在阶段 1 再完善,此处可先放完整 yaml。)

### 验收

```bash
go build ./...
make build    # 可选
```

---

## 阶段 1:配置层

**目标**:Viper 加载 yaml;校验规则;映射到 `pollmux.ServerConfig`。

**依赖**:阶段 0

### 步骤 1.1 — 配置结构体

**文件**: `internal/config/config.go`

**任务**:

1. 定义 ARCHITECTURE.md §10.1 全部 struct:`ServerConfig`, `TunnelConfig`, `ProxyConfig`, `TunnelBinding`(含 `path_prefix`, `strip_prefix`), `ClientConfig`, 等
2. `LoadServer(path string) (*ServerConfig, error)` — viper + mapstructure
3. `LoadClient(path string) (*ClientConfig, error)`
4. `Defaults()` 填充 pollmux 默认值(`PollTimeout=30s`, `SessionTimeout=60s`, …)
5. `(ServerConfig) PollmuxServerConfig(logger *slog.Logger) pollmux.ServerConfig` — 字段映射
6. 辅助:`Version = "0.1.0"` 常量供 `/status` 使用

**产出**: 可加载 example yaml 且无 panic

### 步骤 1.2 — 配置校验

**文件**: `internal/config/validate.go`

**任务**实现 `ValidateServer(cfg *ServerConfig) error`:

1. `root_domain`, `control_host` 非空;`control_host` 以 `root_domain` 结尾
2. `session_timeout >= 2 * poll_timeout`
3. `poll_mode` 为 `""` 或 `"batch"`
4. `tunnels`: token 长度 ≥ 32;token 唯一
5. `(subdomain, path_prefix)` 唯一
6. `path_prefix` 非空时必须 `/` 开头
7. 路径前缀冲突:两条 binding 若 `path_prefix` 互为前缀且 token 不同 → 错误
8. `subdomain: "@"` 合法

`ValidateClient(cfg *ClientConfig) error`:

1. `client_id` 非空
2. `server.url`, `server.token` 非空

**产出**: `internal/config/validate_test.go` 覆盖上述失败路径

### 步骤 1.3 — 配置单测

**文件**: `internal/config/config_test.go`, `validate_test.go`

**用例**:

- 加载 `configs/server.example.yaml` 成功
- `session_timeout: 30s` + `poll_timeout: 30s` → 校验失败且错误信息含 `session_timeout`
- 重复 token → 失败
- 冲突 path_prefix → 失败

### 验收

```bash
go test ./internal/config/...
```

---

## 阶段 2:注册表与路由

**目标**:HostKey + RouteTable + TunnelPool;无 HTTP 也能单测路由逻辑。

**依赖**:阶段 1(仅需 `TunnelBinding` 类型,可先 import config)

### 步骤 2.1 — Host 解析

**文件**: `internal/router/host.go`, `host_test.go`

**任务**:

1. `HostKey(host, rootDomain string) (string, error)`
   - `myapp.builderrors.com` → `"myapp"`
   - `builderrors.com` → `"@"`
   - `evil.com` → `ErrRootMismatch`
   - `a.b.builderrors.com` → `ErrNestedSubdomain`
   -  strip port(`:443`)
2. `HostPolicy(reg *registry.Registry, root, controlHost string) autocert.HostPolicy`
   - 允许 `controlHost` + 各已注册 host key 的 FQDN(含 apex)

**单测**: ARCHITECTURE.md §16 中 `router.HostKey` 行

### 步骤 2.2 — 路径匹配与改写

**文件**: `internal/router/route.go`, `route_test.go`

**任务**:

1. `NormalizePathPrefix(p string) string` — clean, 去尾 `/`(保留 `/`)
2. `StripPathPrefix(path, prefix string) (newPath string, ok bool)`
3. `Route`, `RouteTable` struct
4. `NewRouteTable(bindings []config.TunnelBinding, pools map[string]*registry.TunnelPool) (*RouteTable, error)`
   - 按 host key 分组;同组内按 `len(PathPrefix)` 降序
5. `Match(hostKey, path string) (*Route, error)` — 最长前缀;兜底 `PathPrefix==""`;无匹配返回 error

**单测**:

| 公网 path | 规则 | 命中 |
|---|---|---|
| `/service/auth` | prefix `/service` | strip → `/auth` |
| `/service` | prefix `/service`, strip | `/` |
| `/other` | 仅有兜底 `""` | 兜底 |
| `/api/v1/x` | `/api` 与 `/api/v1` 两条 | 更长者 |

### 步骤 2.3 — Registry 与 TunnelPool

**文件**: `internal/registry/registry.go`, `pool.go` 及 `_test.go`

**任务**:

1. `ClientTunnel` struct(ARCHITECTURE.md §8.1)
2. `Registry`: `byName map[string]*TunnelPool`, `bySess map[string]*ClientTunnel`
3. `Register(t *ClientTunnel, maxPerSubdomain int) error` — 满则 `ErrPoolFull`;同 `client_id` 替换旧 tunnel
4. `GetBySessionID`, `Pool(hostKey)`, `RemoveBySessionID`, `SetLocalHealth`, `Subdomains()`, `Snapshot()`
5. `TunnelPool` + `Balancer` 接口 + `firstAvailable` 实现
6. `Pick(r *http.Request) (*ClientTunnel, bool)` — MVP 第一个 Alive && LocalHealthy

**单测**: 同 ID 替换;409 池满;并发 Register `-race`

### 步骤 2.4 — 启动时编译 RouteTable

**任务**:在 `internal/server` 预留函数(阶段 3 调用):

```go
func buildRouteTable(cfg *config.ServerConfig, reg *registry.Registry) (*router.RouteTable, error)
```

逻辑:对每个 `TunnelBinding` 确保对应 host key 的 pool 存在(空 pool 也可);绑定 Route 指向该 pool。

### 验收

```bash
go test -race ./internal/router/... ./internal/registry/...
```

---

## 阶段 3:服务端控制面

**目标**:Client 能 connect / poll / delete;Hooks 完成注册;`/status` 可读。

**依赖**:阶段 1、2

### 步骤 3.1 — HTTP 错误辅助

**文件**: `internal/server/errors.go`

**任务**: `writeHTTPError`, `writeJSONError` — 统一 JSON 错误体

### 步骤 3.2 — Token 与鉴权

**文件**: `internal/server/auth.go`, `auth_test.go`

**任务**:

1. `TokenStore` 从 `[]TunnelBinding` 构建 `map[token]*TunnelBinding`
2. `Lookup(token)` — `subtle.ConstantTimeCompare`
3. `AuthMiddleware` — Bearer 校验,用于控制面(可选包一层,pollmux Connect 走 Hooks)

### 步骤 3.3 — Server 结构与 pollmux 挂载

**文件**: `internal/server/server.go`

**任务**:

1. `Server` struct: cfg, registry, sessionStore, routes, pollmuxCfg, hooks, controlMux, logger, stopSweeper, …
2. `NewServer(cfg, logger) (*Server, error)` — ValidateServer;NewRegistry;buildRouteTable;NewSessionStore
3. `controlMux` 注册:
   ```go
   pollmux.ConnectHandler(store, pollmuxCfg, hooks)
   pollmux.PollHandler(store, pollmuxCfg, hooks)
   pollmux.DeleteHandler(store, pollmuxCfg, hooks)
   mux.HandleFunc("/status", s.handleStatus) // enabled 时
   ```
4. `SessionIDFunc: func(r *http.Request) string { return mux.Vars(r)["id"] }`
5. `StartSweeper` → 保存 stop 函数
6. `rootHandler`: Host == controlHost → controlMux;否则 → servePublic(阶段 4 实现,可先 501)

### 步骤 3.4 — pollmux Hooks

**文件**: `internal/server/hooks.go`(或写在 server.go)

**任务**:

1. **`authenticateConnect`**
   - 校验 Bearer token
   - 读 `req.Meta["client_id"]`,空则 `pollmux.StatusErrorf(400, ...)`
   - 查 binding;检查 pool 是否已达 `max_clients`(未连接的新 client;重连同 ID 除外) → 409
   - 返回 `meta`: `subdomain`(= host key), `client_id`
2. **`onConnect`**
   - `yamuxSess, err := pollmux.ClientSession(session)`
   - 构造 `ClientTunnel`, `registry.Register`
   - `registry.bySess[session.ID] = tun`
   - `go` 监听 yamux 关闭 → 清理(或依赖 OnDisconnect)
   - `warmCert` goroutine(D12,可先 no-op)
3. **`onPoll`**
   - 读 `X-Local-Health` → `registry.SetLocalHealth`
4. **`onDisconnect`**
   - `registry.RemoveBySessionID`;关闭 yamux

### 步骤 3.5 — `/status`

**文件**: `internal/server/status.go`

**任务**: `handleStatus` 输出 ARCHITECTURE.md §12 JSON;含 `poll_in_flight`, `active_streams`, `local_health`

### 步骤 3.6 — 控制面 httptest

**文件**: `internal/server/server_test.go`

**任务**:

1. 内存启动 Server(随机端口,TLS 可选关闭或用 httptest)
2. connect → 200 + limits + meta
3. poll 空闲 → 204
4. `CloseSession` → 后续 poll 410
5. 错误 token → 401
6. 重复 client 超 max_clients → 409

### 验收

```bash
go test ./internal/server/... -run Control
```

---

## 阶段 4:服务端公网侧

**目标**:公网 HTTP 经域名+路径路由进入隧道;XFF;路径剥前缀。

**依赖**:阶段 3(需真实 ClientTunnel + yamux)

### 步骤 4.1 — bridge

**文件**: `internal/server/bridge.go`

**任务**:

1. `OpenStream(t *ClientTunnel) (net.Conn, error)` — yamux.Open + ActiveStreams 计数
2. `countedConn` — Close 时 Dec
3. `Bridge(a, b net.Conn) error` — 双向 io.Copy(预留,测试可略)

### 步骤 4.2 — ReverseProxy

**文件**: `internal/server/proxy.go`, `proxy_test.go`

**任务**:

1. `proxyCtx{ tunnel, strip string }`
2. `servePublic` — ARCHITECTURE.md §8.4 完整逻辑
3. `newProxy` — Rewrite 路径改写 + SetXForwarded;DialContext 用 proxyCtx.tunnel
4. `proxyErrorHandler` — 502/503/504
5. `FlushInterval: -1`

**单测**(假 yamux 对端):

- 子域名路由 200
- apex + `/service` + strip → 下游收到 `/auth`
- 无路由 404
- 池空/不健康 503
- 伪造 XFF 被剥离

### 步骤 4.3 — 与 rootHandler 接通

**任务**: `rootHandler` 非 controlHost 请求走 `servePublic`

### 验收

```bash
go test ./internal/server/... -run Proxy
```

---

## 阶段 5:客户端

**目标**:Client 连上 Server,Accept 流,转发本地 HTTP,健康检查上报。

**依赖**:阶段 3(控制面可用)

### 步骤 5.1 — 健康检查

**文件**: `internal/client/health.go`, `health_test.go`

**任务**:

1. `Checker` — tcp / http 模式
2. `Run(ctx)` 定时 probe;`Healthy() bool`
3. 状态变化打 info 日志

### 步骤 5.2 — StreamHandler

**文件**: `internal/client/handler.go`, `handler_test.go`

**任务**:

1. `Handle(stream net.Conn)` — DialTimeout 本地 target
2. 双向 `io.Copy`(或 bufio 桥接)
3. `host_header_rewrite`:读第一个 HTTP 请求,改 Host(单请求/流,见 ARCHITECTURE.md §9.2)
4. dial 失败:关 stream,记 warn

### 步骤 5.3 — Client 主循环

**文件**: `internal/client/client.go`

**任务**:

1. `buildConnector` — pollmux.Connector + meta client_id + LocalHealth + zapslog
2. `ReconnectLoop` + `AcceptLoop` — ARCHITECTURE.md §9.1
3. `Run(ctx)` — 先 `health.Run`

### 步骤 5.4 — 客户端联调测试

**文件**: `internal/client/client_test.go`(可选,或依赖阶段 7)

**任务**: against 阶段 3 的 test server:connect 保持 30s;Server 重启后重连

### 验收

```bash
# 终端 1: 测试 server
# 终端 2:
go test ./internal/client/...
# 或手动: httphop-client -config configs/client.example.yaml
```

---

## 阶段 6:入口与 TLS

**目标**:两个可部署二进制;ACME;优雅停机。

**依赖**:阶段 3、4、5

### 步骤 6.1 — TLS 与 :80

**文件**: `internal/server/tls.go`

**任务**:

1. `setupTLS()` — autocert.Manager + HostPolicy
2. `:80` — `HTTPHandler` 挑战 + 301 到 HTTPS
3. `warmCert(subdomain string)` — 后台 GetCertificate

### 步骤 6.2 — Server main

**文件**: `cmd/server/main.go`

**任务**:

1. `-config` flag
2. zap logger
3. `Server.Start()` — :443 + :80
4. 信号 SIGINT/SIGTERM → `Stop(ctx)`:
   - http.Shutdown
   - 所有 session `pollmux.CloseSession`
   - stopSweeper
5. 非零 exit on error

### 步骤 6.3 — Client main

**文件**: `cmd/client/main.go`

**任务**:

1. `-config` flag
2. `Client.Run(context.Background())` until signal
3. cancel ctx on shutdown

### 步骤 6.4 — 本地自签/dev 模式(可选但建议)

**任务**:配置项 `tls.disable` 或 `dev_listen` 用于无 ACME 的本地集成;文档写进 README

### 验收

```bash
make build
# 自签/dev 配置下:
./bin/httphop-server -config configs/server.example.yaml
./bin/httphop-client -config configs/client.example.yaml
curl -k https://.../service/...   # 按路由验证
```

---

## 阶段 7:集成测试与交付

**目标**:进程内 E2E;README 可部署;ARCHITECTURE §16 用例全绿。

**依赖**:阶段 0–6

### 步骤 7.1 — 集成测试 harness

**文件**: `test/integration_test.go`, `test/echo_server.go`(辅助)

**任务**:

1. 随机端口起:echo HTTP 服务、httphop server(dev TLS)、httphop client
2. 实现 ARCHITECTURE.md §16 集成测试 1–11 条
3. 每条测试独立或可 `-parallel`

**关键用例**:

| # | 断言 |
|---|---|
| 1 | 基本转发 body/header |
| 2 | `@` + `/service` + strip → echo 见 `/auth` |
| 3 | 两 token 不串路 |
| 4 | 50 并发总耗时 < 50×单请求(宽松阈值) |
| 5 | Server 重启 Client 重连 |
| 6 | 410 后秒级重连 |
| 7 | Limits min 生效 |
| 8 | XFF 真实 IP |
| 9 | 停 echo → local_health down + 503 |
| 10 | max_clients 409 |
| 11 | protocol_version 426 |

### 步骤 7.2 — README 完善

**任务**:

1. 架构图(简版)
2. **连接模型**(附录 A 摘要):Server–Client 隧道不随公网连接数增长
3. builderrors.com 示例配置(server `@` + `/service`, client 指向 127.0.0.1)
4. 上下行吞吐不对称
5. systemd unit 示例(可选)

### 步骤 7.3 — Makefile 与 CI(可选)

**任务**: `make test` 含 integration;GitHub Actions `go test -race ./...`

### 验收

```bash
go test -race ./...
# 或
go test -race ./test/...
```

**MVP 完成定义**:上述命令通过;手工清单 ARCHITECTURE.md §16「手工/端到端」可在真实域名抽检。

---

## 附录 A:连接模型速查

实现阶段 4、5 时保持以下不变量:

```
公网用户 N 个 TCP  ──►  HttpHop Server
                              │
                              │  每个 Client 仅 1 条 pollmux 隧道
                              │  隧道内 ≤ max_streams_per_tunnel 条 yamux 流
                              ▼
                         HttpHop Client
                              │
                              │  每条 yamux 流 → 1 次 Dial 本地 TCP
                              ▼
                         本地 HTTP M 个连接( M ≤ 在途流数 )
```

- **Server↔Client 隧道数** = 已注册 Client 数(与公网 N 无关)
- **Client↔本地连接数** = 当前在途请求数(与公网 N 对应,经 yamux  multiplex 后展开)
- 超过 `max_streams_per_tunnel` → 公网 **503**,不新建隧道

---

## 附录 B:阶段依赖图

```
阶段 0 骨架
   │
   ▼
阶段 1 配置 ─────────────────────────┐
   │                                  │
   ▼                                  │
阶段 2 注册表/路由                     │
   │                                  │
   ▼                                  │
阶段 3 控制面 ◄────────────────────────┘
   │
   ├──────────────┐
   ▼              ▼
阶段 4 公网侧   阶段 5 客户端
   │              │
   └──────┬───────┘
          ▼
      阶段 6 入口/TLS
          │
          ▼
      阶段 7 集成/E2E
```

**可并行**:阶段 4 与 5 在阶段 3 完成后可两人并行;阶段 7 前必须合并。

---

## 附:建议提交粒度(Git)

| 提交 | 内容 |
|---|---|
| 1 | 阶段 0 |
| 2 | 阶段 1 config |
| 3 | 阶段 2 router + registry |
| 4 | 阶段 3 server control |
| 5 | 阶段 4 proxy |
| 6 | 阶段 5 client |
| 7 | 阶段 6 cmd + tls |
| 8 | 阶段 7 integration + README |

每提交后 `go test -race ./...` 必须通过。
