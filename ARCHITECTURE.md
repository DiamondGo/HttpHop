# HttpHop 详细设计文档(v6)

> **文档状态**:设计定稿,尚未开始实现。仓库 `/home/kexie/source/HttpHop` 目前仅有本文档。
> **传输层**:HTTP 长轮询虚拟连接 + yamux 多路复用由共享库 [`github.com/DiamondGo/pollmux`](https://github.com/DiamondGo/pollmux) 提供(库本身已完成;HttpBroker 迁移验证中)。HttpHop **不**再维护 `internal/transport`,只写应用层 glue。
> **本文目标**:详细到可以直接照着写代码 —— 包含结构体定义、函数签名、HttpHop 特有逻辑、配置样例、分阶段实现顺序与验收标准。wire 协议与传输机制见 pollmux 的 `DESIGN.md` / `README.md`。

---

## 目录

1. [背景与目标](#一背景与目标)
2. [已确定的决策](#二已确定的决策)
3. [设计评审:发现的问题与改进](#三设计评审发现的问题与改进)
4. [总体架构](#四总体架构)
5. [传输层:引用 pollmux](#五传输层引用-pollmux)
6. [HttpHop 控制面约定](#六httphop-控制面约定)
7. [心跳与失效检测](#七心跳与失效检测)
8. [服务端详细设计](#八服务端详细设计)
9. [客户端详细设计](#九客户端详细设计)
10. [配置](#十配置)
11. [错误与状态码](#十一错误与状态码)
12. [日志与可观测性](#十二日志与可观测性)
13. [包结构与文件清单](#十三包结构与文件清单)
14. [依赖](#十四依赖)
15. [分阶段实现顺序与验收](#十五分阶段实现顺序与验收) — 详见 [plans/IMPLEMENTATION.md](plans/IMPLEMENTATION.md)
16. [测试计划](#十六测试计划)
17. [MVP 范围与推后项](#十七mvp-范围与推后项)
18. [已知限制](#十八已知限制)

---

## 一、背景与目标

在空仓库 `/home/kexie/source/HttpHop` 中从零构建 **HttpHop**:

- **Server(服务端)**:部署在有公网 IP 和域名、但本地资源很少的机器上。对外提供 HTTPS 服务,把收到的公网 HTTP 请求转发给已连接的 Client。
- **Client(客户端)**:部署在内网(无法被外部主动连接)、但本地算力/服务很强的机器上。接收 Server 转发过来的请求,转发到本地可访问的 HTTP 服务,再把响应原路返回。

典型场景:把内网的强力 HTTP 服务通过一台轻量公网机器暴露到互联网 —— 支持 **子域名整站映射**(`myapp.example.com/*`)与 **根域路径映射**(`example.com/service/*` → 内网 `/*`),不需要端口映射,也不暴露内网,且**不需要**在公网另装 nginx。

**参考项目**:`~/source/HttpBroker` 是同一作者的兄弟项目,实现了 SOCKS5-over-HTTP 隧道。HttpHop 与 HttpBroker 在"长轮询虚拟连接 + yamux"层高度相似,共同部分已抽成 **`github.com/DiamondGo/pollmux`**(含 A1–A5、B1 第一层、D3/D10 等修复)。HttpHop 只实现 HttpBroker 没有的应用层:公网 `ReverseProxy`、子域名路由、多租户注册表、autocert 等。

**角色对应关系**:

| HttpBroker | HttpHop | 说明 |
|---|---|---|
| broker(3 角色中枢) | Server 的控制面 | 会话管理、长轮询端点 |
| consumer(常驻代理节点) | **不存在** | HttpHop 的"公网调用方"是一次性 HTTP 请求,不是常驻节点 |
| provider(内网服务提供方) | **Client** | 架构上完全等价 |
| `relay.bridgeStream` | Server 的 `proxy.go` / `bridge.go` | 从独立 goroutine 折叠进同步的公网请求处理器 |

---

## 二、已确定的决策

| # | 决策 | 依据 |
|---|---|---|
| 1 | 实现语言 **Go**(≥ 1.21) | 单文件静态编译、goroutine 模型天然适配、与 HttpBroker 一致可复用代码 |
| 2 | 公网侧支持 **域名映射 + 路径映射**,不依赖 nginx 等外部反代 | 用户明确要求:如 `builderrors.com/service/*` → 内网 `/*`,须由 HttpHop 自身完成 Host 选择与 URI 改写 |
| 3 | Server 支持**多个 Client 同时接入**;域名维度按 Host(子域名或根域)路由,路径维度按**最长前缀匹配** | 与 #2 配套;同一 Host 上可挂多条不同 `path_prefix` 的隧道 |
| 4 | 传输层**必须用长轮询**,并**把长轮询本身当作心跳** | 用户明确要求:服务端要**尽早**知道某条路径不能服务了;由 pollmux 实现 |
| 5 | 长轮询传输层使用 **`github.com/DiamondGo/pollmux`**,不复制 HttpBroker 旧 `internal/transport` | 缺陷修一次、两边受益;`EnableKeepAlive=false` 等约束封进库 API;服务端 **Limits 下发**消除双端配置漂移 |
| 6 | 公网侧用 **`httputil.ReverseProxy` + 自定义 `DialContext`**,并把桥接逻辑抽进 `bridge.go` 为裸 TCP 预留 | 见 §3.C。裸桥接方案会被 HTTP/2 打崩、拿不到真实客户端 IP、且**无法支撑后续的会话保持需求** |
| 7 | 后续要做**负载均衡 + 会话保持**;MVP **只预留结构、不实现策略** | 见 §3.F。预留成本约几十行,不预留将来要同时改三处 |
| 8 | 下行吞吐 MVP **只做"加大 poll 缓冲"这一层**;流式 chunked poll 推到 MVP 之后 | 见 §3.B。由 pollmux `PollBufferSize` 可配,默认 256KB |
| 9 | **前置依赖**:pollmux API 经 HttpBroker 迁移验证后再发版;开发期可用 `go.mod` 的 `replace` 指本地路径 | 见 pollmux `DESIGN.md` 决策 #1 |

---

## 三、设计评审:发现的问题与改进

本节是对 HttpBroker 实际实现的逐行审查结果,是本文档大部分设计约束的来源。**实现时请把这一节当作 checklist。**

### A. 从 HttpBroker 继承的缺陷(必须修掉)

#### A1. 客户端 HTTP 超时全为 0,静默断网检测不出来 ⚠️ 违背需求 #3

**证据**:`HttpBroker/internal/provider/client.go:67-80`

```go
httpTransport := &http.Transport{
    ResponseHeaderTimeout: 0,   // 无限
    IdleConnTimeout:       0,
}
httpClient := &http.Client{ Timeout: 0 }   // 无限
```

**问题**:如果链路是"黑洞式"失效(中间设备静默丢包、NAT 表项过期、对端机器直接掉电,没有 RST/FIN),客户端的长轮询请求会**永远挂住** —— `pollLoop` 既不返回也不报错,`transportFailedCh` 永不关闭,客户端会一直以为自己连着。只有"快速失败"场景(连接被拒、TLS 失败、明确的 5xx)才会被立即发现。

**修复**:

- `pollClient.Transport.ResponseHeaderTimeout = poll_timeout + poll_grace`(30s + 10s = 40s)。
  **理论依据**:健康的服务端在**最迟 `poll_timeout` 时刻一定会返回 204**(pollmux `PollHandler` 契约),所以超过 `poll_timeout + 宽限` 还没收到响应头,就是确定性的链路故障证据。
- `Transport.DialContext` 用 `&net.Dialer{Timeout: 10s, KeepAlive: 15s}`,开启 TCP keepalive。
- **发送请求用独立的、更短超时的 `sendClient`**(`Timeout: 15s`)—— 发送不该被长轮询的宽松超时约束。
- 结果:**空闲期最坏检测延迟 ≈ 40s;有流量时 ≈ 15s**。

#### A2. 服务端 1MB 体上限 vs 客户端无上限写缓冲 → 隧道被整条重置

**证据**:`HttpBroker/internal/broker/server.go:252` 是 `http.MaxBytesReader(w, r.Body, 1<<20)`,而 `httpconn.go:172-180` 的 `flushLoop` 把**整个无上限的 `writeBuf` 一次性发出**。

**问题**:yamux 每流默认 256KB 窗口,并发 10 条流在途数据就可能超过 1MB → 服务端 400 → `doSend` 看到非 200/204 → `signalTransportFailed()` → **整条隧道断开重连,所有在途请求全挂**。

**修复**(由 pollmux 实现):

- 服务端在 connect 时通过 **`limits.max_send_bytes`** 下发权威分片上限;客户端实际取 `min(本地 max_send_chunk, 服务端值)`。
- `flushLoop` 每次最多取该上限(默认 512KB,以服务端为准)。
- 服务端超限返回 **413**(不是 400)。守规矩的客户端在 `min()` 之后**不应**再触发 413 —— 若出现,视为**协议违规/对端 bug**,记录日志并关闭会话重连,**不做减半重试**(那条带状态的重试路径是双端各配一份上限时的权宜之计,参数下发后整段删除)。

#### A3. 服务端侧失效检测太慢(最坏 6 分钟)⚠️ 违背需求 #3

**证据**:`server.go:52-54` 的 `SessionTimeout` 默认 5 分钟;`server.go:369` 的清理 ticker 是 1 分钟。

**修复**:

- `session_timeout` 默认改为 **`poll_timeout × 2` = 60s**。
  **理论依据**:健康 Client 收到 204 后**立即**重新发起 poll(`poll_interval = 0`),所以服务端每 `poll_timeout` 至少能看到一次 poll;连续两个周期看不到就是确凿失效。
- 清理 ticker 从 1 分钟改为 **5 秒**。
- `Session` 增加 `pollInFlight int32`(原子):**有 poll 挂在服务端 = TCP 连接活着 = Client 活着**。这是比 `LastActive` 更强的实时信号 —— 当 TCP 收到 RST/FIN 时,挂起的 poll 会**立即**返回,`pollInFlight` 归零,服务端**瞬时**感知。
- 结果:**6 分钟 → 最坏 65 秒;有 TCP 通知时接近瞬时**。

#### A4. `BufferedPipe` 无背压,资源少的服务端有内存风险

**证据**:`pipe.go:39` 是 `p.buf = append(p.buf, data...)`,完全无上限。

**修复**(由 pollmux 实现;**原稿 64KB 窗口是错误的**):

- yamux **强制** `MaxStreamWindowSize ≥ 256KB`(`initialStreamWindow`),设为 64KB 会让 `yamux.Client()`/`Server()` **直接报错**,隧道建不起来。降窗口也不是加背压 —— 它会把单流吞吐摁回 64KB/RTT,抵消 B1 的 poll 缓冲修复。
- pollmux 保持地板值 **256KB**;控内存靠**并发流数**,不靠缩窗口。
- HttpHop 应用层设 `max_streams_per_tunnel`(默认 256),超出直接返回 503。
- 最坏内存 ≈ 256KB × 典型并发流数,**约 64MB/隧道**(多租户需乘以隧道数);`BufferedPipe` 加高水位告警(`HighWaterWarn`),仅观测不阻塞。

#### A5. 服务端关闭会话时,客户端不会及时重连(新发现)

**证据**:`server.go:298-302` —— `ReadAvailable` 返回 `io.EOF`(pipe 已关闭)时,服务端返回 **204**。而 `httpconn.go:311-313` 里客户端把 204 当作"无数据,继续轮询"。

**问题**:服务端主动关闭会话(优雅停机、yamux 会话死亡、管理员踢下线)后,客户端会**一直空转轮询**,直到 `session_timeout` 把会话从注册表里清掉、客户端才收到 404。这期间隧道实际已经不可用,但客户端不知道要重连。

**修复**:pollmux `PollHandler` 在 `err == io.EOF` 时返回 **410 Gone**(而不是 204);客户端把 410 与 404/401 同等对待 —— 触发 `TransportFailed()` 立即进入重连。

### B. 吞吐瓶颈

#### B1. 下行吞吐被"64KB / RTT"卡死

**证据**:`server.go:285` 的 poll 读缓冲写死 `64KB`;`httpconn.go:336-342` 在收到数据后**立即**重新发起 poll(不 sleep)。也就是说下行每个 RTT 最多搬运 64KB。

| RTT | 下行吞吐上限 |
|---|---|
| 5ms(同城) | ~12.8 MB/s |
| 50ms(跨省) | ~1.28 MB/s |
| 150ms(跨境) | ~430 KB/s |

**上行方向没有这个问题**:`flushLoop` 每次把累积的全部缓冲一次发出,是自适应的(A2 的分片修复后变成 512KB/RTT,仍远好于 64KB)。**这个上下行不对称必须写进 README。**

**修复分两层。已决定:MVP 只做第一层。**

**第一层(MVP)**:`poll_buffer_size` 从写死的 64KB 改为可配,默认 **256KB**。改动是一个数字加一个配置项,零风险,吞吐 ×4(150ms RTT 下 ~430KB/s → ~1.7MB/s)。成本是每个在途 poll 多占 256KB(用 `sync.Pool` 复用缓冲区可进一步降低 GC 压力)。

**第二层(MVP 之后)**:流式 chunked poll 响应。服务端保持响应体打开,用 `Transfer-Encoding: chunked` 在最长 `poll_timeout` 内持续写入多个带 4 字节长度前缀的数据块;客户端边读边喂给 `readPipe`。下行从"每 RTT 一次往返"变成**接近连续的流**,吞吐不再受 RTT 限制。心跳语义完全保留(响应仍在 `poll_timeout` 到期时结束;空闲时每 15s 发一个 0 长度 keepalive 块,`ResponseHeaderTimeout` 换成基于块间隔的读超时)。

**推后理由**:(a) 要重写 pollmux 的 `pollLoop` 和 `PollHandler` 两个地基函数,应等 MVP 跑通、有测试覆盖后再动;(b) 前置 CDN/nginx 的某些配置会缓冲 chunked 响应导致该模式直接失效,需要实测。
**为它预留**:`poll_mode: batch | stream` 现在就加进配置结构体,MVP 只接受 `batch`,传 `stream` 直接启动失败并提示未实现。

### C. 公网侧改用 `httputil.ReverseProxy`(推翻早期的裸桥接方案)

早期方案是"**先窥探 Host 头,然后裸 `io.Copy` 桥接**":自己接管 TCP 监听,`http.ReadRequest` 只解析第一个请求以取得 Host,之后把连接当作不透明字节流搬运。

**它的三个实际问题:**

- **C1. HTTP/2 会直接把它打崩。** `autocert.Manager.TLSConfig()` 默认在 `NextProtos` 通告 `h2`。浏览器一旦协商成 HTTP/2,连接上流过的是二进制帧(以 `PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n` 开头),`http.ReadRequest` 解析失败 → 连接直接断。必须手动把 `NextProtos` 削成 `["http/1.1","acme-tls/1"]` 才能不崩,代价是永久放弃 HTTP/2。
- **C2. 连接复用可能导致路由错误。** 只看第一个请求的 Host,keep-alive 上后续请求全部盲送同一隧道。浏览器在证书覆盖多域名时会做连接合并 → 路由错误。
- **C3. 没有地方注入 `X-Forwarded-For`。** 字节原样搬运,没有插入 header 的时机;内网服务看到的永远是 Client 自己的地址。

**替代方案:标准 `http.Server` + `ReverseProxy` + 自定义 `DialContext`** —— 核心思想是:**yamux 流本身就是一个 `net.Conn`,那就让 Go 已经写好的、久经考验的 HTTP 栈跑在它上面。**

| 维度 | 裸桥接 | ReverseProxy |
|---|---|---|
| HTTP/2 | ❌ 直接崩,必须关掉 h2 | ✅ 标准库处理;公网可开 h2,隧道内仍 HTTP/1.1 |
| 真实客户端 IP | ❌ 拿不到 | ✅ `pr.SetXForwarded()` 一行,且自动清空伪造的入站 XFF |
| 逐跳 header 剥离 | ❌ 原样透传 | ✅ 按 RFC 7230 自动处理 |
| keep-alive 上的路由 | ⚠️ 只按第一个请求 | ✅ 每请求独立决策 |
| WebSocket | ✅ 天然 | ✅ 支持(**前提:`Transport` 必须是 `*http.Transport`**,所以必须用自定义 `DialContext` 而非手写 `RoundTripper`) |
| SSE / 流式 | ✅ 天然 | ✅ 需设 `FlushInterval: -1`,**不设会被缓冲** |
| 非 HTTP 协议 | ✅ 任意 TCP | ❌ 仅 HTTP |
| 代码量 | 更多 | 更少 |

**代价与对策**:失去转发任意非 HTTP 协议的能力。对策是把"从 `*ClientTunnel` 开一条 yamux 流 + 与给定 `net.Conn` 双向桥接"抽成 `internal/server/bridge.go` 里的独立函数 —— `ReverseProxy` 的 `DialContext` 用它的前半段,将来做裸 TCP/UDP 端口转发时复用整个函数。MVP 不增加工作量,只是把代码放对位置。

### D. 缺失的功能

| # | 缺失项 | 处理方式 |
|---|---|---|
| D1 | `X-Forwarded-For` / `-Proto` / `-Host` 注入 + 剥离伪造的入站值 | `ReverseProxy.Rewrite` 里调 `pr.SetXForwarded()`(`Rewrite` 相比旧 `Director` 的设计目的就是这个) |
| D2 | Host 头改写(某些内网服务按 Host 做虚拟主机) | Client 侧配置 `host_header_rewrite`,可选,空值表示保持原 Host |
| D3 | **本地服务健康检查** —— 隧道通但内网服务挂了 | Client 周期探测本地目标,结果搭车在 poll 请求的 `X-Local-Health` 头上报;服务端更新 `ClientTunnel.LocalHealthy`,不健康时对该子域名直接返回 503。**补齐需求 #3 的最后一块** |
| D4 | ACME HTTP-01 挑战需要的 :80 监听 + HTTPS 跳转 | `autocert.Manager.HTTPHandler(redirectHandler)` 挂 :80 |
| D5 | 隧道 API 与公网服务共存于 :443 | 保留一个**控制域名**(如 `tunnel.example.com`);handler 先判 Host,命中控制域名交给 gorilla/mux,否则走隧道查找。单端口对防火墙最友好 |
| D6 | 公网监听加固 | `ReadHeaderTimeout: 10s`(防 Slowloris)、`MaxHeaderBytes: 64KB`、每隧道并发流上限 256 |
| D7 | 请求超时不能误伤流式响应 | 只对"开流到收到响应头"设 60s(`Transport.ResponseHeaderTimeout`),**响应体阶段不设超时**,SSE/WebSocket/大文件不受影响 |
| D8 | 优雅的错误响应 | 见 §11 状态码表,统一由 `ErrorHandler` + `writeHTTPError` 产出 |
| D9 | `/status` 观测面 | 见 §12 |
| D10 | 客户端 `poll_interval` 默认值 | **0**(收到响应立即重新 poll)。HttpBroker 允许在此 sleep,只会白白增加延迟 —— 等待本就应发生在服务端的长轮询里 |
| D11 | 前置 CDN/nginx 兼容性 | 中间代理可能截断 30s 长轮询。`poll_timeout` 可配并在 README 给出建议;这也是 B1 保留 `batch` 模式的原因 |
| D12 | 首次访问时证书还没签发 | Client 注册成功后**后台预热**:调一次 `manager.GetCertificate(&tls.ClientHelloInfo{ServerName: sub})`,避免第一个真实请求卡在 ACME 签发上 |

### E. 工程化

- 传输层单测由 **pollmux** 承担(`go test -race ./...` 已覆盖 A1/A2/A5 等);HttpHop 只写应用层单测与集成测试。
- 新增端到端集成测试:进程内起 server + client + 本地 echo 服务。
- 模块路径 `github.com/DiamondGo/HttpHop`(与 HttpBroker / pollmux 一致)。Go ≥ 1.21。
- 日志边界:pollmux 用 `*slog.Logger`;HttpHop 继续用 zap,经 `go.uber.org/zap/exp/zapslog` 桥接。

### F. 为后续的负载均衡 + 会话保持预留结构(MVP 只预留,不实现策略)

**背景**:后续需求是同一子域名接入多个内网后端,并做会话保持(同一用户的多个请求固定路由到同一后端)。

**这个需求也是 C 节选 ReverseProxy 的决定性理由。** 会话保持需要四件事,裸桥接做不到其中三件:

| 需要的能力 | 裸桥接 | ReverseProxy |
|---|---|---|
| 每请求独立选后端 | ❌ 只在连接建立时决策一次 | ✅ 每请求走一遍 `Rewrite` |
| 读 Cookie/Header 取亲和性 key | ❌ 后续请求根本不解析 | ✅ 拿到完整 `*http.Request` |
| 往响应里种 sticky cookie | ❌ **根本不可能**(响应方向是纯 `io.Copy`) | ✅ `ModifyResponse` 钩子 |
| 后端故障时重试到另一个后端 | ❌ 字节已盲拷进流,无法回退 | ✅ `ErrorHandler` + 可重放的 `GetBody` |

裸桥接唯一能做的是 IP hash,但出口 NAT、移动端 IP 变化、后端数量变化都会破坏它。

**MVP 要预留的三样东西**(行为与不做完全一致,多写约几十行):

1. `Registry.byName` 用 `map[string]*TunnelPool`,池内 MVP 恒 1 个成员。
2. **`ClientTunnel.ID` 必须跨重连稳定** —— 用 Client 配置里的固定 `client_id`,**绝对不能用 session_id**(每次重连都会变,sticky 亲和性会随之断掉)。这点现在不定,以后补会非常别扭。
3. `Balancer` 接口抽出来,MVP 只实现 `firstAvailable`。

**注册语义随之修改**:早期写的是"同一子域名已有活跃会话则拒绝 409"。有了多后端后,多个 Client 注册同一子域名是**正常情况**。改为 `max_clients_per_subdomain`(默认 1,>1 即开启多后端),超过上限才 409。

**明确不在 MVP 内**:轮询/最少连接策略、sticky cookie 读写、后端健康剔除、故障重试。

### G. 公网路由:域名映射 + 路径映射(MVP 必须)

HttpHop **自行**完成公网入口的路由与 URI 改写,**不**假设前置 nginx/Caddy。两层正交:

| 层 | 输入 | 作用 |
|---|---|---|
| **域名映射** | `Host` 头 | 选定根域上的**路由组**(子域名或 apex 根域) |
| **路径映射** | `URL.Path` | 在该 Host 上按 **`path_prefix` 最长前缀匹配** 选隧道,并按规则改写路径 |

#### G0. 部署建议:优先整子域名映射

**推荐默认方案**:为每个内网服务分配独立子域名(`myapp.builderrors.com`),**不**配置 `path_prefix`。公网路径与内网路径一致,后端跳转、Cookie、静态资源无需额外处理,Client 也不感知公网路由规则。

**备选方案**:必须在 apex 根域上挂路径时(如 `builderrors.com/service/*`),使用 `subdomain: "@"` + `path_prefix` + `strip_prefix`。Server 在转发时会剥前缀,并在响应侧改写 **`Location`** 与 **`Set-Cookie` Path**(等价 nginx `proxy_redirect` / `proxy_cookie_path`),以覆盖常见 302 与会话 Cookie。**不改写 HTML/JS 正文**;静态资源或前端路由仍可能需要应用配置公网 base path。**除非必须占用 apex 路径,否则优先用子域名。**

| 方式 | 示例 | 路径是否一致 | 推荐 |
|---|---|---|---|
| 子域名整站 | `myapp.builderrors.com/auth` → 内网 `/auth` | ✅ | **首选** |
| apex + 路径前缀 | `builderrors.com/service/auth` → 内网 `/auth` | ❌(需剥/补前缀) | 仅当必须 |

#### G1. Host 键:子域名与 apex 根域

沿用 `root_domain`(如 `builderrors.com`)。`TunnelBinding.subdomain` 取值:

| 配置值 | 匹配的 Host | 内部 registry 键 |
|---|---|---|
| `"myapp"` | `myapp.builderrors.com` | `"myapp"` |
| `"@"` | **`builderrors.com`**(apex,无子域前缀) | `"@"` |
| 省略 `path_prefix` | 该 Host 下**所有路径**(该组的默认/兜底路由) | — |

> apex 用 `"@"` 而非空字符串,避免与「未配置」混淆。`router.HostKey(host, rootDomain)` 返回 `(key, nil)` 或错误。

**典型场景(builderrors.com)**:

```yaml
# 推荐:整子域名映射
tunnels:
  - subdomain: "myapp"
    token: "..."
    max_clients: 1
# 效果: https://myapp.builderrors.com/foo → 内网 /foo (路径不改写)

# 备选:apex 路径前缀 (仅当必须占用 builderrors.com/service 时)
tunnels:
  - subdomain: "@"
    path_prefix: "/service"
    strip_prefix: true
    token: "..."
    max_clients: 1
# 效果: https://builderrors.com/service/auth → 内网 /auth
# 响应头 Location / Set-Cookie Path 由 Server 自动补回 /service 前缀
```

另见 §G0。完整配置示例见 [README.md](README.md)。

#### G2. 路径规则字段

每条 `TunnelBinding` 可选路径字段(MVP):

```go
type TunnelBinding struct {
    Subdomain   string // "myapp" 或 "@"
    PathPrefix  string // 默认 "" = 匹配该 Host 的全部路径(兜底)
    StripPrefix bool   // true:转发前去掉 PathPrefix(见 G3)
    Token       string
    MaxClients  int
}
```

**匹配规则**:

1. 解析 Host → host key(`"myapp"` / `"@"`)。
2. 在该 key 下,取所有已注册且 `Alive()` 的 binding 的 `PathPrefix`,做**最长前缀匹配**。
3. 若无任何 `PathPrefix` 命中,使用该 Host 下 `PathPrefix == ""` 的**兜底** binding(若存在)。
4. 若仍无 → **404** `no route for host/path`。
5. 启动时校验:同一 `(subdomain, path_prefix)` 不重复;**禁止**两条非空前缀互为前缀且指向不同 token(配置冲突,启动失败)。

**`strip_prefix` 语义**(在 `ReverseProxy.Rewrite` 里改 `pr.Out.URL.Path`,Client **无需**知道公网前缀):

| 公网 Path | PathPrefix | StripPrefix | 转发 Path |
|---|---|---|---|
| `/service/auth` | `/service` | true | `/auth` |
| `/service` | `/service` | true | `/` |
| `/service/auth` | `/service` | false | `/service/auth` |
| `/api/v1/x` | `/api/v1` | true | `/x` |

Query string 原样保留。`PathPrefix` 必须 `path.Clean` 后以 `/` 开头;`/service` 与 `/service/` 在匹配时等价(统一去掉尾部 `/` 再比,**除**前缀恰为 `/` 的情况)。

#### G3. 与 Client 的关系

- **路径改写在 Server 侧完成**,经 yamux 流发给 Client 的已是改写后的 HTTP 请求;Client 仍只 `Dial(local.target)`,**不**配置 `path_prefix`,**不需要**知道公网 `path_prefix`。
- 使用 `strip_prefix` 时,Server **`ModifyResponse`** 还会把后端的 `Location` / `Set-Cookie Path` 补回公网前缀(见 G0);Client 不参与。
- 一个 Client 实例仍对应**一个** `local.target`;同一 Host 上多条路径前缀若指向同一 token/subdomain,共享一条隧道;若指向不同 token,则对应不同 Client 实例。
- `host_header_rewrite`(D2)与路径映射正交:默认保持公网 Host(`builderrors.com`);若内网服务依赖 Host,可配 rewrite。

#### G4. 路由数据结构

```go
// internal/router/route.go
type Route struct {
    HostKey    string // "myapp" | "@"
    PathPrefix string // "" 表示兜底
    Strip      bool
    Pool       *registry.TunnelPool // 由 binding 的 subdomain/token 解析
}

// 启动时由 []TunnelBinding 编译;运行时只读。
type RouteTable struct { /* per-host sorted by len(PathPrefix) desc */ }

func (t *RouteTable) Match(hostKey, path string) (*Route, error)
```

`Registry` 仍按 subdomain(= host key)维护 `TunnelPool`;`RouteTable` 持有到 pool 的引用,**不**重复会话状态。

#### G5. 证书

`HostPolicy` 除控制域外,须为所有出现过的 host key 签发证书:各子域名 + 若存在 `"@"` binding 则包含 **apex** `root_domain`。

---

## 四、总体架构

```
                              公网 Internet
                                   │
                                   │  HTTPS   Host: myapp.httphop.io
                                   ▼
    ┌──────────────────────────────────────────────────────────────┐
    │                          SERVER                              │
    │                                                              │
    │  :80  autocert HTTP-01 挑战 + 301 跳转 HTTPS                  │
    │                                                              │
    │  :443 标准 http.Server(autocert TLS,HostPolicy 限制签发)     │
    │          │                                                   │
    │          ├─ Host == tunnel.example.com ─→ gorilla/mux 控制面  │
    │          │      pollmux Connect/Poll/Delete handler 挂载     │
    │          │      GET    /status              健康观测(HttpHop)│
    │          │                                                   │
    │          └─ 其他 Host ─→ servePublic()                        │
    │                  │ 1. router.HostKey(Host) → "myapp"|"@"      │
    │                  │ 2. router.RouteTable.Match(key, Path)       │
    │                  │ 3. pool.Pick(r) 选后端(MVP: firstAvail)     │
    │                  │ 4. 健康 / 并发上限检查                        │
    │                  │ 5. context(tunnel + PathRewrite)           │
    │                  │ 6. ReverseProxy(Rewrite 里改 Path + XFF)    │
    │                  ▼                                           │
    │           httputil.ReverseProxy                               │
    │             Rewrite:  SetXForwarded + Path 改写 + Host        │
    │             Transport.DialContext → bridge.OpenStream()       │
    │             FlushInterval: -1(SSE 实时)                      │
    │             ErrorHandler → 502 / 503 / 504                    │
    │                  │                                           │
    │                  ▼  yamux.Open()(Server 是 yamux.Client)     │
    │           Registry map[subdomain]*TunnelPool                  │
    └──────────────────────────────────────────────────────────────┘
                                   │
              虚拟连接 = pollmux 长轮询(读写分离)
       Client 侧 pollmux.Conn  ←──────────────→  Server 侧 pollmux.Session
       (pollLoop + flushLoop)                    (PollHandler + yamux Client)
                                   │
    ┌──────────────────────────────────────────────────────────────┐
    │                          CLIENT                              │
    │  pollmux.Connector → Conn; ReconnectLoop + AcceptLoop         │
    │  yamux.Server —— Accept() 循环                                │
    │          │                                                   │
    │          ▼ 每条流 → net.DialTimeout("tcp", local_target)      │
    │  本地 HTTP 服务                                                │
    │                                                              │
    │  health.Checker ── 每 15s 探测 ──→ atomic.Bool                │
    │                                       └→ poll 请求的 X-Local-Health │
    └──────────────────────────────────────────────────────────────┘
```

### 单个公网请求的完整路径

**例 A — 子域名,无路径前缀**:`https://myapp.builderrors.com/auth`

**例 B — apex + 路径前缀**:`https://builderrors.com/service/auth` → 内网 `/auth`

```
公网调用方
  │ ① HTTPS  Host + Path
  ▼
Server :443
  │ ② servePublic: HostKey + RouteTable.Match → Pick 后端
  │ ③ context 携带 tunnel 与 PathRewrite(strip /service 等)
  ▼
ReverseProxy.Rewrite  (XFF + 改写 pr.Out.URL.Path + 保持/改写 Host)
  │ ④ DialContext → bridge.OpenStream
  ▼
…(pollmux 隧道,与 v5 相同)…
  ▼
Client → 127.0.0.1:8080/auth   ← 例 B 已是 /auth,非 /service/auth
```

---

## 五、传输层:引用 pollmux

HTTP 长轮询虚拟连接、yamux 配置、connect/poll/delete 端点、客户端 `pollLoop`/`flushLoop`、会话扫描等**全部在 pollmux 内实现**。HttpHop 不维护 `internal/transport/`。

**设计原则**:pollmux 只管"字节怎么在两台机器之间流动",不管这些字节是什么、也不管两端是什么角色。HttpHop 的应用语义(`client_id` → `subdomain`、本地健康、公网路由)走 **`meta` 映射**和 **`Hooks` 回调**。

### 5.1 pollmux 提供的核心类型

| 类型 | 用途 |
|---|---|
| `pollmux.Conn` | 客户端侧虚拟连接(`io.ReadWriteCloser` + `TransportFailed()` + `Limits()`) |
| `pollmux.Connector` | 发起 connect、启动 pollLoop |
| `pollmux.Session` | 服务端侧纯传输会话(无 subdomain 等应用字段) |
| `pollmux.SessionStore` | 按 `session_id` 索引传输会话 |
| `pollmux.ServerConfig` | 服务端传输参数(`PollTimeout`、`MaxSendBytes`、`PollBufferSize` 等) |
| `pollmux.Hooks` | 应用插桩:`Authenticate` / `OnConnect` / `OnPoll` / `OnDisconnect` |
| `pollmux.ConnectHandler` / `PollHandler` / `DeleteHandler` | 可挂载的 HTTP handler |
| `pollmux.YamuxConfig()` / `ClientSession()` / `ServerSession()` | 封装 `EnableKeepAlive=false` 等约束 |
| `pollmux.ReconnectLoop` / `pollmux.AcceptLoop` | 客户端重连与 Accept 四路 select |

完整 API 与 wire 格式见 `github.com/DiamondGo/pollmux` 源码与 `DESIGN.md`。

### 5.2 yamux 角色与配置

**角色分配**(与 HttpBroker provider 侧一致):

- **Server = yamux Client** —— `pollmux.ClientSession(session)` 后 `Open()` 流,把每个公网请求推给 Client。
- **Client = yamux Server** —— `pollmux.ServerSession(conn)` 后 `Accept()` 流。

**必须**通过 pollmux 建会话,不要自行调 `yamux.Client()`/`Server()`:

```go
// Server 侧(OnConnect 里,Session 已注册进 SessionStore 之后)
yamuxSess, err := pollmux.ClientSession(pollSession)

// Client 侧(runSession 里)
yamuxSess, err := pollmux.ServerSession(conn)
```

`pollmux.YamuxConfig()` 已设置:

- `EnableKeepAlive = false`(正确性前提,见 §7)
- `MaxStreamWindowSize = 256KB`(yamux 地板,不可再小)
- `KeepAliveInterval = 30s`(yamux 校验要求,keepalive 关闭时也非零)

### 5.3 HttpHop 服务端:SessionStore + Hooks

```go
type Server struct {
    cfg          config.ServerConfig
    registry     *registry.Registry      // 应用层:子域名 → TunnelPool
    sessionStore *pollmux.SessionStore   // 传输层:session_id → Session
    pollmuxCfg   pollmux.ServerConfig
    hooks        pollmux.Hooks
    auth         *TokenStore
    proxy        *httputil.ReverseProxy
    logger       *zap.Logger
    // ...
}

func (s *Server) buildHooks() pollmux.Hooks {
    return pollmux.Hooks{
        Authenticate: s.authenticateConnect,  // token → subdomain;校验 meta["client_id"]
        OnConnect:    s.onConnect,            // 启动 yamux、登记 ClientTunnel、预热证书
        OnPoll:       s.onPoll,               // 处理 X-Local-Health
        OnDisconnect: s.onDisconnect,         // 从 TunnelPool 移除
    }
}
```

控制面路由挂载 pollmux handler(示例用 gorilla/mux):

```go
mux.Handle("/tunnel/connect", pollmux.ConnectHandler(s.sessionStore, s.pollmuxCfg, s.hooks))
mux.Handle("/tunnel/{id}/poll", pollmux.PollHandler(s.sessionStore, s.pollmuxCfg, s.hooks))
mux.Handle("/tunnel/{id}", pollmux.DeleteHandler(s.sessionStore, s.pollmuxCfg, s.hooks))
// SessionIDFunc 零值读 PathValue("id");mux 用户传 func(r) string { return mux.Vars(r)["id"] }
```

`pollmuxCfg` 由 HttpHop 配置映射:

```go
pollmux.ServerConfig{
    PollTimeout:    cfg.Tunnel.PollTimeout,
    SessionTimeout: cfg.Tunnel.SessionTimeout,
    SweepInterval:  cfg.Tunnel.SweepInterval,
    CoalesceWindow: cfg.Tunnel.CoalesceWindow,
    PollBufferSize: cfg.Tunnel.PollBufferSize,
    MaxSendBytes:   cfg.Tunnel.MaxSendBytes,
    HighWaterWarn:  cfg.Tunnel.HighWaterWarn,
    PollMode:       cfg.Tunnel.PollMode,
    Logger:         slog.New(zapslog.NewHandler(logger.Core())), // nil 禁用
}
```

会话扫描用 `pollmux.StartSweeper(st, cfg, hooks)`,驱逐时 `OnDisconnect` 清理应用层注册表。

### 5.4 HttpHop 客户端:Connector + ReconnectLoop

```go
connector := &pollmux.Connector{
    BaseURL:            cfg.Server.URL,
    AuthToken:          cfg.Server.Token,
    Meta:               map[string]string{"client_id": cfg.ClientID},
    PollInterval:       cfg.Transport.PollInterval,   // 默认 0(D10)
    PollGrace:          cfg.Transport.PollGrace,      // A1:加在服务端下发的 poll_timeout 上
    SendTimeout:        cfg.Transport.SendTimeout,
    CoalesceWindow:     cfg.Transport.CoalesceWindow,
    MaxSendChunk:       cfg.Transport.MaxSendChunk,   // 实际 min(此值, limits.MaxSendBytes)
    LocalHealth:        health.Healthy,               // D3
    InsecureSkipVerify: cfg.Server.InsecureSkipVerify,
    Logger:             slog.New(zapslog.NewHandler(logger.Core())),
}

loop := &pollmux.ReconnectLoop{
    Connect: func(ctx context.Context) (pollmux.Conn, error) {
        return connector.Connect(ctx)
    },
    Serve: func(ctx context.Context, conn pollmux.Conn) pollmux.Outcome {
        sess, err := pollmux.ServerSession(conn)
        if err != nil { return pollmux.OutcomeTransportFailed }
        defer sess.Close()
        return pollmux.AcceptLoop(ctx, sess, conn, handler.Handle)
    },
    Logger: slogLogger,
}
return loop.Run(ctx)
```

connect 成功后,客户端按服务端下发的 `limits` 自动设置 `ResponseHeaderTimeout = poll_timeout + poll_grace`(A1),并在启动时校验 `poll_interval` 不会导致被误判掉线。

### 5.5 §3 缺陷在 pollmux 中的对应

| 缺陷 | pollmux 中的实现 |
|---|---|
| A1 客户端无超时 | 独立 poll/send client;`ResponseHeaderTimeout = limits.PollTimeout + PollGrace` |
| A2 体上限不匹配 | connect 下发 `limits.max_send_bytes`;客户端 `min()` 后分片发送 |
| A3 服务端检测慢 | `SessionTimeout = 2×PollTimeout`;5s sweeper;`PollInFlight()` |
| A4 无背压 | 256KB 窗口 + 高水位告警;HttpHop 用 `max_streams_per_tunnel` 限流 |
| A5 EOF 与 204 混淆 | 会话关闭回 **410**,客户端立即重连 |
| B1 64KB poll 缓冲 | `PollBufferSize` 可配,默认 256KB |
| D3 本地健康 | `Connector.LocalHealth` → `X-Local-Health`;`Hooks.OnPoll` 接收 |
| D10 poll_interval | 默认 0;过大时 connect 后自检失败 |

---

## 六、HttpHop 控制面约定

wire 协议(端点路径、读写分离 header、状态码语义、`limits` 下发、`protocol_version`)由 **pollmux 定义**,详见 pollmux `protocol.go` / `DESIGN.md` §4.4。本节只描述 **HttpHop 在 pollmux 之上追加的应用语义**。

### 6.1 端点与鉴权

所有端点挂在**控制域名**下(如 `https://tunnel.example.com`),要求 `Authorization: Bearer <token>`。路径前缀默认 `/tunnel`,与 pollmux 一致:

| 方法 | 路径 | pollmux handler |
|---|---|---|
| POST | `/tunnel/connect` | `ConnectHandler` |
| POST | `/tunnel/{id}/poll` | `PollHandler` |
| DELETE | `/tunnel/{id}` | `DeleteHandler` |

HttpHop 另提供 `GET /status`(§12),不走 pollmux。

### 6.2 connect 的 meta 映射

**客户端请求体**(pollmux `ConnectRequest`):

```json
{
  "protocol_version": 1,
  "meta": { "client_id": "home-gpu-01" }
}
```

**服务端 `Hooks.Authenticate`**:

```
1. 校验 Bearer token → TokenStore.Lookup → 得到 subdomain、max_clients
2. 校验 meta["client_id"] 非空;缺则 StatusErrorf(400, ...)
3. 返回 meta 合并结果:{"subdomain": "myapp", "host_key": "myapp"}  // subdomain=host_key;apex 为 "@"
4. 若该 subdomain 已达 max_clients → StatusErrorf(409, ...)  // 【F】
```

**成功响应**(pollmux `ConnectResponse`,HttpHop 关心的字段):

```json
{
  "protocol_version": 1,
  "session_id": "9f2c...e41a",
  "limits": {
    "max_send_bytes": 1048576,
    "poll_timeout_ms": 30000,
    "session_timeout_ms": 60000,
    "poll_buffer_bytes": 262144
  },
  "meta": { "subdomain": "myapp" }
}
```

客户端从 `meta["subdomain"]` 得知分配的子域名;传输参数以 **`limits`** 为权威,不再依赖本地 `poll_timeout` 与服务端配置对齐。

### 6.3 `Hooks.OnConnect` 处理顺序

pollmux 保证:**先**把 `Session` 注册进 `SessionStore`,**再**调 `OnConnect`(修复 HttpBroker 的预注册竞态)。HttpHop 在 `OnConnect` 内:

```
1. yamuxSess := pollmux.ClientSession(session)
2. tun := &ClientTunnel{ID: meta["client_id"], Subdomain: meta["subdomain"], Session: session, Yamux: yamuxSess, ...}
3. registry.Register(tun, maxClients)  // 同 client_id 视为重连 → 替换
4. go acceptYamuxStreams(yamuxSess, tun)  // Server 侧通常只需等 yamux 关闭;公网请求通过 Open() 推流
5. warmCert(subdomain)                    // 【D12】
6. 返回 nil
```

### 6.4 poll 搭车:`X-Local-Health`

pollmux 不解释此头,在 `Hooks.OnPoll` 里交给 HttpHop:

```go
func (s *Server) onPoll(session *pollmux.Session, r *http.Request) {
    if h := r.Header.Get(pollmux.HeaderLocalHealth); h != "" {
        sub := session.Meta()["subdomain"]
        cid := session.Meta()["client_id"]
        s.registry.SetLocalHealth(sub, cid, h == "ok")
    }
}
```

读写分离(`X-Send-Only` / `X-Receive-Only`)、长轮询 204 心跳、410 会话关闭、413 协议违规等语义均由 pollmux `PollHandler` 实现,HttpHop 无需重写。

### 6.5 `Hooks.OnDisconnect`

传输会话关闭或被 sweeper 驱逐时调用。HttpHop 从 `TunnelPool` 移除对应 `ClientTunnel`,关闭 yamux(若仍存活)。`reason` 区分客户端 DELETE、服务端停机、超时驱逐等。

### 6.6 时序图

**正常请求转发**

```
公网      Server                              Client            本地服务
 │          │                                   │                  │
 │          │◀────── poll(挂起,等 30s) ────────│                  │
 │─请求────▶│                                   │                  │
 │          │ yamux.Open() → session.Write      │                  │
 │          │──── 200 + 数据 ──────────────────▶│                  │
 │          │◀───── poll(立即重发) ────────────│  yamux.Accept()  │
 │          │                                   │──── dial ───────▶│
 │          │                                   │◀─── 响应 ────────│
 │          │◀──── X-Send-Only POST(响应数据) ─│                  │
 │◀─响应────│                                   │                  │
```

**空闲心跳**

```
Client                     Server (pollmux PollHandler)
  │──── poll ──────────────▶│  ReadAvailable 阻塞 poll_timeout
  │                         │
  │◀─────── 204 ────────────│  超时,无数据
  │──── poll(立即) ───────▶│  LastActive / PollInFlight 更新
```

**Client 掉线的两种情形**

```
情形一(有 TCP FIN/RST,如 kill 进程):
  Server: 挂起的 poll 立即返回 → PollInFlight 归零 → 【瞬时感知】
          sweeper 在 session_timeout(60s) 后 OnDisconnect 驱逐

情形二(静默黑洞,如拔网线 / iptables DROP):
  Client: pollClient 在 poll_timeout+grace(40s) 后超时
          → TransportFailed → 退避重连
  Server: 挂起的 poll 一直挂着,直到 TCP 超时;
          LastActive 停止更新 → 60s 后 sweeper 驱逐
```

---

## 七、心跳与失效检测

需求 #3 的机制层回答。**三层,各自覆盖不同的失效模式**:

| 层 | 机制 | 检测延迟 | 覆盖的失效 |
|---|---|---|---|
| **1. 客户端自检** | pollmux `pollLoop` 是持续的出站探针。任何请求失败(连接拒绝、TLS 失败、非 2xx、**A1 的响应头超时**)→ `TransportFailed()` → 退避重连 | 快速失败:**秒级**<br>静默黑洞:**≈40s**(A1) | 网络中断、服务端重启、会话失效 |
| **2. 服务端自检** | pollmux 每次 poll 更新 `LastActive` 并维护 `PollInFlight()`。健康 Client 保证每 `poll_timeout` 至少来一次 poll | 有 TCP FIN/RST:**接近瞬时**(A3)<br>其他:**≤60s**(`session_timeout`,5s sweeper) | Client 进程被杀、掉电、单向不通 |
| **3. 本地服务健康** | Client 每 15s 探测本地目标,结果搭车在 `X-Local-Health` 上报;不健康时服务端对该子域名直接返回 503 | **≈15s**(D3) | **隧道通但内网服务本身挂了** |

**为什么这三层缺一不可**:第 1 层保护的是客户端自身(它需要知道何时重连);第 2 层保护的是服务端(它需要知道何时停止把请求送进死路);第 3 层保护的是端到端可用性(前两层都健康但服务不可用)。

**关键坑(必须遵守)**:yamux 自带 keepalive 必须在两端都关掉。pollmux `YamuxConfig()` / `ClientSession()` / `ServerSession()` 已封装此约束 —— **不要绕过它们自行建 yamux 会话**。长轮询挂起时 PING 无法在 yamux 的 `ConnectionWriteTimeout`(10s)内完成往返,会产生假的"连接已死"信号。

---

## 八、服务端详细设计

### 8.1 `internal/registry/` —— 注册表与后端池

```go
// registry.go
type ClientTunnel struct {
    ID          string        // 【F】跨重连稳定,来自 Client 配置的 client_id
    Subdomain   string
    Session     *pollmux.Session   // 传输层会话;PollInFlight/LastActive 从这里读
    Yamux       *yamux.Session
    ConnectedAt time.Time
    RemoteAddr  string

    LocalHealthy  atomic.Bool   // 【D3】
    ActiveStreams atomic.Int64  // 【D6】并发流计数
}

func (t *ClientTunnel) Alive() bool     // yamux 未关闭 && Session 仍存活
func (t *ClientTunnel) Close() error

type Registry struct {
    mu      sync.RWMutex
    byName  map[string]*TunnelPool   // 【F】子域名 → 后端池
    bySess  map[string]*ClientTunnel // session_id → ClientTunnel,供 /status 与 OnPoll 快速查找
}

func (r *Registry) Register(t *ClientTunnel, maxPerSubdomain int) error  // 409 时返回 ErrPoolFull
func (r *Registry) GetBySessionID(id string) (*ClientTunnel, bool)
func (r *Registry) Pool(subdomain string) (*TunnelPool, bool)
func (r *Registry) Subdomains() []string                 // 供 autocert HostPolicy
func (r *Registry) SetLocalHealth(sub, clientID string, ok bool)
func (r *Registry) RemoveBySessionID(sessionID string)
func (r *Registry) Snapshot() []TunnelStatus              // 【D9】供 /status
```

> 传输层会话过期扫描由 **`pollmux.StartSweeper`** 负责,驱逐时 `Hooks.OnDisconnect` 调用 `Registry.RemoveBySessionID`。应用层 Registry **不再**实现独立的 `Sweep`。

```go
// pool.go —— 【F】LB 结构预留
type TunnelPool struct {
    mu      sync.RWMutex
    members []*ClientTunnel
    bal     Balancer
}

func (p *TunnelPool) Add(t *ClientTunnel, max int) error  // 同 ID 视为重连 → 替换
func (p *TunnelPool) Remove(id string)
func (p *TunnelPool) Pick(r *http.Request) (*ClientTunnel, bool)
func (p *TunnelPool) ByID(id string) *ClientTunnel        // 供将来的 sticky cookie
func (p *TunnelPool) Len() int

type Balancer interface {
    Pick(members []*ClientTunnel, r *http.Request) *ClientTunnel
}

// MVP 唯一实现:返回第一个 Alive() && LocalHealthy 的成员。
type firstAvailable struct{}

// 后续实现(不在 MVP):roundRobin / leastConn / consistentHash / stickyCookie
```

### 8.2 `internal/router/` —— Host 与路径路由

```go
// host.go
// HostKey 从 Host 头解析路由组键。
//   "myapp.builderrors.com:443" + root "builderrors.com"  →  "myapp", nil
//   "builderrors.com"                                         →  "@", nil
//   "evil.com"                                                →  "", ErrRootMismatch
//   "a.b.builderrors.com"                                     →  "", ErrNestedSubdomain
func HostKey(host, rootDomain string) (string, error)

func HostPolicy(reg *registry.Registry, root, controlHost string) autocert.HostPolicy
// 除 controlHost 外,为每个已注册 HostKey 签发:子域名 FQDN;键 "@" 时含 apex root。
```

```go
// route.go —— §3.G
type PathRewrite struct {
    StripPrefix string // 非空则已从 Path 去掉此前缀
}

func StripPathPrefix(path, prefix string) (newPath string, ok bool)

type Route struct {
    HostKey    string
    PathPrefix string
    Strip      bool
    Pool       *registry.TunnelPool
}

type RouteTable struct { /* compiled from bindings + registry */ }

func NewRouteTable(bindings []config.TunnelBinding, reg *registry.Registry) (*RouteTable, error)
func (t *RouteTable) Match(hostKey, path string) (*Route, error) // 最长前缀;兜底 PathPrefix==""
func (t *RouteTable) Validate() error // 启动时:重复键、前缀冲突
```

### 8.3 `internal/server/server.go` —— 控制面

```go
type Server struct {
    cfg          config.ServerConfig
    registry     *registry.Registry
    sessionStore *pollmux.SessionStore
    routes       *router.RouteTable       // §3.G 编译自 tunnels + registry
    pollmuxCfg   pollmux.ServerConfig
    hooks        pollmux.Hooks
    auth         *TokenStore
    proxy        *httputil.ReverseProxy
    stopSweeper  func()              // pollmux.StartSweeper 返回
    logger       *zap.Logger
    done         chan struct{}
    stopOnce     sync.Once
}

func NewServer(cfg, logger) *Server
func (s *Server) Start() error          // 起 :80 和 :443、挂载 pollmux handler、StartSweeper
func (s *Server) Stop(ctx) error        // 优雅停机(顺序见下)

func (s *Server) handleStatus(w, r)     // §12,HttpHop 自有

// pollmux Hooks 实现(§6)
func (s *Server) authenticateConnect(r, req) (meta, error)
func (s *Server) onConnect(session, meta) error
func (s *Server) onPoll(session, r)
func (s *Server) onDisconnect(session, reason)
```

**顶层 handler 的 Host 分流(D5)**:

```go
func (s *Server) rootHandler(w http.ResponseWriter, r *http.Request) {
    if stripPort(r.Host) == s.cfg.ControlHost {
        s.controlMux.ServeHTTP(w, r)     // gorilla/mux:/tunnel/*, /status
        return
    }
    s.servePublic(w, r)                   // 走隧道
}
```

**优雅停机顺序**:

```
1. httpSrv.Shutdown(ctx) —— 停止接受新连接,在途请求排空
2. 对每个 pollmux.Session 调 pollmux.CloseSession(store, hooks, s, ReasonServerClose)
   → pipe 关闭 → 客户端 poll 拿到 410(A5) → 立即重连
3. stopSweeper(); close(s.done)
```

### 8.4 `internal/server/proxy.go` —— 公网请求处理

**关键设计**:**在外层 handler 里做 Host+Path 路由与健康检查**,把 `tunnel` 与路径改写规则塞进 context;`Rewrite` 执行 URI 改写 + XFF;`DialContext` 开流。

```go
type ctxKey struct{}

type proxyCtx struct {
    tunnel *registry.ClientTunnel
    strip  string // 非空:Rewrite 时去掉此前缀
}

func (s *Server) servePublic(w http.ResponseWriter, r *http.Request) {
    hostKey, err := router.HostKey(r.Host, s.cfg.RootDomain)
    if err != nil { writeHTTPError(w, 400, "invalid host"); return }

    route, err := s.routes.Match(hostKey, r.URL.Path)
    if err != nil { writeHTTPError(w, 404, "no route for host/path"); return }

    tun, ok := route.Pool.Pick(r)
    if !ok { writeHTTPError(w, 503, "no available backend"); return }

    if !tun.LocalHealthy.Load() {
        writeHTTPError(w, 503, "backend local service unhealthy"); return
    }
    if tun.ActiveStreams.Load() >= int64(s.cfg.Tunnel.MaxStreamsPerTunnel) {
        writeHTTPError(w, 503, "tunnel stream limit reached"); return
    }

    pctx := proxyCtx{tunnel: tun}
    if route.Strip && route.PathPrefix != "" {
        pctx.strip = route.PathPrefix
    }
    r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, pctx))
    s.proxy.ServeHTTP(w, r)
}

func (s *Server) newProxy() *httputil.ReverseProxy {
    return &httputil.ReverseProxy{
        Rewrite: func(pr *httputil.ProxyRequest) {
            pr.Out.URL.Scheme = "http"
            pr.Out.URL.Host   = "tunnel"
            pr.Out.Host       = pr.In.Host
            if pctx, ok := pr.In.Context().Value(ctxKey{}).(proxyCtx); ok && pctx.strip != "" {
                if stripped, ok := router.StripPathPrefix(pr.Out.URL.Path, pctx.strip); ok {
                    pr.Out.URL.Path = stripped
                }
            }
            pr.SetXForwarded()
        },
        Transport: &http.Transport{
            DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
                pctx, _ := ctx.Value(ctxKey{}).(proxyCtx)
                if pctx.tunnel == nil { return nil, errNoTunnel }
                return bridge.OpenStream(pctx.tunnel)
            },
            DisableKeepAlives:     true,
            ForceAttemptHTTP2:     false,
            ResponseHeaderTimeout: s.cfg.Proxy.ResponseHeaderTimeout,
        },
        FlushInterval: -1,
        ErrorHandler:  s.proxyErrorHandler,
        ErrorLog:      zap.NewStdLog(s.logger),
    }
}
```

```go
func (s *Server) proxyErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
    switch {
    case errors.Is(err, context.DeadlineExceeded): writeHTTPError(w, 504, "backend timeout")
    case errors.Is(err, errNoTunnel):              writeHTTPError(w, 503, "tunnel gone")
    default:                                        writeHTTPError(w, 502, "backend error")
    }
}
```

> **关于 WebSocket**:`ReverseProxy` 自 Go 1.12 起处理 101 升级 —— 但**前提是 `Transport` 必须是 `*http.Transport`**。这就是为什么这里用自定义 `DialContext` 而不是手写 `RoundTripper`。实现时不要"优化"成后者。

### 8.5 `internal/server/bridge.go` —— 桥接原语(为裸 TCP 预留)

```go
// OpenStream 在隧道上开一条 yamux 流,并用计数包装返回,
// 使 ActiveStreams 在流关闭时自动减一。
func OpenStream(t *registry.ClientTunnel) (net.Conn, error) {
    stream, err := t.Yamux.Open()
    if err != nil { return nil, err }
    t.ActiveStreams.Add(1)
    return &countedConn{Conn: stream, tunnel: t}, nil
}

// Bridge 在两个连接之间双向拷贝,任一方向结束即收敛。
// MVP 的 ReverseProxy 路径用不到它 —— 它是为将来的裸 TCP/UDP 端口转发预留的
// (见 §3.C"代价与对策")。届时:conn := listener.Accept();
// stream := OpenStream(tun); Bridge(conn, stream)。
func Bridge(a, b net.Conn) error
```

### 8.6 `internal/server/auth.go`

```go
type TunnelBinding struct {
    Subdomain   string
    PathPrefix  string
    StripPrefix bool
    Token       string
    MaxClients  int    // 【F】默认 1
}

type TokenStore struct {
    byToken map[string]*TunnelBinding   // 启动时从配置构建,只读
}

func (ts *TokenStore) Lookup(token string) (*TunnelBinding, bool)

// AuthMiddleware 校验 Authorization: Bearer <token>,把 binding 放进 context。
// 可选的 unauthorized_redirect 模式:对未授权请求返回 302 到诱饵 URL 而非 401,
// 让隧道端点更难被扫描器指纹识别(移植自 HttpBroker,非 MVP 必需)。
func AuthMiddleware(ts *TokenStore, cfg RedirectConfig, next http.Handler) http.Handler
```

**安全要点**:token → 子域名的绑定**在服务端配置里写死**,Client 不能自选子域名 —— 防止子域名抢注/劫持。token 比较用 `subtle.ConstantTimeCompare`。

### 8.7 `internal/server/tls.go`

```go
func (s *Server) setupTLS() (*tls.Config, http.Handler) {
    m := &autocert.Manager{
        Cache:      autocert.DirCache(s.cfg.TLS.CacheDir),
        Prompt:     autocert.AcceptTOS,
        Email:      s.cfg.TLS.Email,
        HostPolicy: router.HostPolicy(s.registry, s.cfg.RootDomain, s.cfg.ControlHost),
    }
    tlsCfg := m.TLSConfig()   // 默认包含 h2 —— 保留它,ReverseProxy 方案支持 HTTP/2
    return tlsCfg, m.HTTPHandler(httpsRedirectHandler())   // 【D4】:80
}

// 【D12】Client 注册成功后后台调用,避免第一个真实请求卡在 ACME 签发上
func (s *Server) warmCert(subdomain string)
```

---

## 九、客户端详细设计

### 9.1 `internal/client/client.go` —— ReconnectLoop + AcceptLoop

使用 pollmux 的 `ReconnectLoop` 与 `AcceptLoop`,不再手写退避与四路 select。

```go
type Client struct {
    cfg     config.ClientConfig
    handler *StreamHandler
    health  *health.Checker
    logger  *zap.Logger
}

// Run 阻塞直到 ctx 取消。传输层重连由 pollmux.ReconnectLoop 驱动。
func (c *Client) Run(ctx context.Context) error {
    go c.health.Run(ctx)          // 【D3】健康检查独立于隧道生命周期常驻

    slogLogger := slog.New(zapslog.NewHandler(c.logger.Core()))
    connector := c.buildConnector(slogLogger)

    loop := &pollmux.ReconnectLoop{
        Connect: func(ctx context.Context) (pollmux.Conn, error) {
            return connector.Connect(ctx)
        },
        Serve: c.serveSession,
        Logger: slogLogger,
    }
    return loop.Run(ctx)
}

func (c *Client) serveSession(ctx context.Context, conn pollmux.Conn) pollmux.Outcome {
    sess, err := pollmux.ServerSession(conn)
    if err != nil { return pollmux.OutcomeTransportFailed }
    defer sess.Close()
    return pollmux.AcceptLoop(ctx, sess, conn, c.handler.Handle)
}

func (c *Client) buildConnector(logger *slog.Logger) *pollmux.Connector {
    return &pollmux.Connector{
        BaseURL:            c.cfg.Server.URL,
        AuthToken:          c.cfg.Server.Token,
        Meta:               map[string]string{"client_id": c.cfg.ClientID},
        PollInterval:       c.cfg.Transport.PollInterval,
        PollGrace:          c.cfg.Transport.PollGrace,
        SendTimeout:        c.cfg.Transport.SendTimeout,
        CoalesceWindow:     c.cfg.Transport.CoalesceWindow,
        MaxSendChunk:       c.cfg.Transport.MaxSendChunk,
        LocalHealth:        c.health.Healthy,
        InsecureSkipVerify: c.cfg.Server.InsecureSkipVerify,
        Logger:             logger,
    }
}
```

> `AcceptLoop` 同时监听 ctx 取消、`TransportFailed()`、yamux `CloseChan`、Accept 错误 —— 缺任何一个都会导致重连不及时。`OutcomePeerClosed` 与 `OutcomeTransportFailed` 决定退避策略(见 pollmux `reconnect.go`)。

### 9.2 `internal/client/handler.go` —— 转发到本地服务

```go
type StreamHandler struct {
    target      string          // "127.0.0.1:8080"
    hostRewrite string          // 【D2】空 = 保持原 Host
    dialTimeout time.Duration
    logger      *zap.Logger
}

func (h *StreamHandler) Handle(stream net.Conn) {
    defer stream.Close()

    local, err := net.DialTimeout("tcp", h.target, h.dialTimeout)
    if err != nil {
        // 直接关闭流即可:服务端的 Transport 会看到连接错误,
        // ErrorHandler 产出 502。此处只需记日志说明原因。
        h.logger.Warn("dial local target failed", zap.String("target", h.target), zap.Error(err))
        return
    }
    defer local.Close()

    // 双向拷贝。若配置了 host_header_rewrite,则需在上行方向解析并改写第一个
    // 请求的 Host 头(仅第一个;DisableKeepAlives 保证一条流只有一个请求)。
    bridgeBidirectional(stream, local)
}
```

> **一条流一个请求**:服务端 `Transport` 设了 `DisableKeepAlives: true`,所以每条 yamux 流上只跑一个 HTTP 请求/响应对。这大大简化了 `host_header_rewrite` 的实现 —— 只需处理第一个(也是唯一一个)请求。

### 9.3 `internal/client/health.go` —— 本地服务健康检查(D3)

```go
type Checker struct {
    target   string          // "127.0.0.1:8080"
    mode     string          // "tcp" | "http"
    path     string          // mode == "http" 时的探测路径,如 "/healthz"
    interval time.Duration   // 默认 15s
    timeout  time.Duration   // 默认 3s
    healthy  atomic.Bool
    logger   *zap.Logger
}

func (c *Checker) Healthy() bool { return c.healthy.Load() }

// Run 常驻探测,独立于隧道生命周期(隧道断了本地服务可能还好着,反之亦然)。
// 启动时立即探一次,避免刚启动的那 15s 里状态是错的。
func (c *Checker) Run(ctx context.Context) {
    c.probe()
    t := time.NewTicker(c.interval)
    defer t.Stop()
    for {
        select {
        case <-ctx.Done(): return
        case <-t.C:        c.probe()
        }
    }
}

// probe:tcp 模式做 DialTimeout;http 模式发 GET {path} 并要求 2xx/3xx。
// 状态变化时打 info 日志(不要每次探测都打)。
func (c *Checker) probe()
```

结果通过 `pollmux.Connector.LocalHealth` 传给 pollmux 客户端,由 poll 搭车在 `X-Local-Health` 头上报 —— **不需要额外的控制流或控制协议**,因为 poll 请求本来就每 `poll_timeout` 至少来一次。

---

## 十、配置

### 10.1 Go 结构体(viper + mapstructure)

```go
// ===== 服务端 =====
type ServerConfig struct {
    PublicListen  string          `mapstructure:"public_listen"`   // ":443"
    HTTPListen    string          `mapstructure:"http_listen"`     // ":80",ACME + 跳转
    RootDomain    string          `mapstructure:"root_domain"`     // "httphop.io"
    ControlHost   string          `mapstructure:"control_host"`    // "tunnel.httphop.io"

    TLS     TLSConfig      `mapstructure:"tls"`
    Tunnel  TunnelConfig   `mapstructure:"tunnel"`
    Proxy   ProxyConfig    `mapstructure:"proxy"`
    Tunnels []TunnelBinding `mapstructure:"tunnels"`   // token → 子域名 绑定表
    Status  StatusConfig   `mapstructure:"status"`
    Logging LoggingConfig  `mapstructure:"logging"`
}

type TunnelConfig struct {
    PollTimeout    time.Duration `mapstructure:"poll_timeout"`     // 30s → pollmux ServerConfig
    SessionTimeout time.Duration `mapstructure:"session_timeout"`  // 【A3】60s
    SweepInterval  time.Duration `mapstructure:"sweep_interval"`   // 【A3】5s
    CoalesceWindow time.Duration `mapstructure:"coalesce_window"`  // 2ms
    PollBufferSize int           `mapstructure:"poll_buffer_size"` // 【B1】262144
    MaxSendBytes   int           `mapstructure:"max_send_bytes"`   // 【A2】1048576,connect 时下发给客户端
    HighWaterWarn  int           `mapstructure:"high_water_warn"`  // BufferedPipe 高水位告警,0=禁用
    PollMode       string        `mapstructure:"poll_mode"`        // 【B1】"batch";"stream" 未实现
    MaxStreamsPerTunnel int      `mapstructure:"max_streams_per_tunnel"` // 【D6】256,应用层限流
}

type ProxyConfig struct {
    ResponseHeaderTimeout time.Duration `mapstructure:"response_header_timeout"` // 【D7】60s
    ReadHeaderTimeout     time.Duration `mapstructure:"read_header_timeout"`     // 【D6】10s
    MaxHeaderBytes        int           `mapstructure:"max_header_bytes"`        // 【D6】65536
}

type TunnelBinding struct {
    Subdomain   string // "myapp" 或 "@" (apex)
    PathPrefix  string `mapstructure:"path_prefix"`  // 默认 "" = 该 Host 兜底
    StripPrefix bool   `mapstructure:"strip_prefix"` // 默认 false
    Token       string
    MaxClients  int    `mapstructure:"max_clients"`   // 【F】默认 1
}

// ===== 客户端 =====
type ClientConfig struct {
    ClientID  string           `mapstructure:"client_id"`   // 【F】必填,跨重连稳定
    Server    ServerRef        `mapstructure:"server"`
    Local     LocalConfig      `mapstructure:"local"`
    Transport TransportConfig  `mapstructure:"transport"`
    Health    HealthConfig     `mapstructure:"health"`
    Logging   LoggingConfig    `mapstructure:"logging"`
}

type ServerRef struct {
    URL                string `mapstructure:"url"`
    Token              string `mapstructure:"token"`
    InsecureSkipVerify bool   `mapstructure:"insecure_skip_verify"`
}

type LocalConfig struct {
    Target      string `mapstructure:"target"`              // "127.0.0.1:8080"
    HostRewrite string `mapstructure:"host_header_rewrite"` // 【D2】空 = 保持原 Host
}

type TransportConfig struct {
    PollInterval   time.Duration `mapstructure:"poll_interval"`    // 【D10】0
    PollGrace      time.Duration `mapstructure:"poll_grace"`       // 【A1】10s,加在服务端下发的 poll_timeout 上
    SendTimeout    time.Duration `mapstructure:"send_timeout"`     // 【A1】15s
    DialTimeout    time.Duration `mapstructure:"dial_timeout"`     // 10s,拨本地服务用
    CoalesceWindow time.Duration `mapstructure:"coalesce_window"`  // 2ms
    MaxSendChunk   int           `mapstructure:"max_send_chunk"`   // 【A2】524288;实际 min(此值, limits.max_send_bytes)
}

type HealthConfig struct {
    Enabled  bool          `mapstructure:"enabled"`   // 【D3】true
    Mode     string        `mapstructure:"mode"`      // "tcp" | "http"
    Path     string        `mapstructure:"path"`      // "/healthz"
    Interval time.Duration `mapstructure:"interval"`  // 15s
    Timeout  time.Duration `mapstructure:"timeout"`   // 3s
}
```

> 客户端**不再**配置 `poll_timeout` / `session_timeout` —— 由 connect 响应的 `limits` 下发。pollmux 在 connect 后自检 `poll_interval` 是否会导致误判掉线。

**配置校验**(启动时执行,失败直接退出并给出可操作的提示):

- `client_id` 非空;`root_domain` / `control_host` 非空且 `control_host` 以 `root_domain` 结尾。
- `session_timeout ≥ poll_timeout × 2`,否则健康 Client 会被误判掉线 —— **这是最容易配错的一项,必须校验并给出明确报错**(映射到 `pollmux.ServerConfig`)。
- `poll_mode == "batch"`(或空),否则报"stream 模式尚未实现"。
- `tunnels` 里 token 不重复且长度 ≥ 32;`(subdomain, path_prefix)` 不重复;路径前缀冲突校验(§3.G)。
- `path_prefix` 若非空,必须以 `/` 开头;`subdomain: "@"` 时须已配置 apex 证书需求。

### 10.2 `configs/server.example.yaml`

```yaml
public_listen: ":443"
http_listen:   ":80"          # ACME HTTP-01 挑战 + HTTPS 跳转
root_domain:   "httphop.io"
control_host:  "tunnel.httphop.io"

tls:
  cache_dir: "/var/lib/httphop/certs"
  email:     "admin@httphop.io"

tunnel:
  poll_timeout:    30s
  session_timeout: 60s        # 必须 ≥ poll_timeout × 2
  sweep_interval:  5s
  coalesce_window: 2ms
  poll_buffer_size: 262144    # 256KB。加大可提升下行吞吐(约 buf/RTT)
  max_send_bytes:   1048576   # connect 时下发;客户端 max_send_chunk 不得超过此值
  high_water_warn:  0         # 0=禁用;可设如 4194304(4MB) 打告警
  poll_mode: "batch"          # "stream" 尚未实现
  max_streams_per_tunnel: 256 # 应用层并发流上限(D6)

proxy:
  response_header_timeout: 60s  # 只约束响应头阶段,不影响 SSE/WebSocket/大文件
  read_header_timeout:     10s
  max_header_bytes:        65536

tunnels:
  # --- 场景: builderrors.com/service/* → 内网 /* ---
  - subdomain: "@"
    path_prefix: "/service"
    strip_prefix: true
    token: "REPLACE_WITH_32B_RANDOM_TOKEN_AAAA"
    max_clients: 1

  # --- 场景: 独立子域名,路径原样转发 ---
  - subdomain: "myapp"
    token: "REPLACE_WITH_32B_RANDOM_TOKEN_BBBB"
    max_clients: 1

  # --- 同 Host 多前缀(可选): 最长前缀优先 ---
  # - subdomain: "@"
  #   path_prefix: "/api"
  #   strip_prefix: true
  #   token: "..."

status:
  enabled: false              # 默认关闭
  listen:  "127.0.0.1:9090"   # 仅本机可见

logging:
  level: "info"
```

### 10.3 `configs/client.example.yaml`

```yaml
client_id: "home-gpu-01"      # 必须跨重启稳定 —— 将来的会话保持依赖它

server:
  url:   "https://tunnel.httphop.io"
  token: "REPLACE_WITH_32B_RANDOM_TOKEN_AAAA"
  insecure_skip_verify: false

local:
  target: "127.0.0.1:8080"
  host_header_rewrite: ""     # 空 = 把公网请求的原始 Host 原样传给本地服务

transport:
  poll_interval:   0s         # 0 = 收到响应立即重发;须满足 pollmux 启动自检
  poll_grace:      10s        # ResponseHeaderTimeout = limits.poll_timeout + poll_grace
  send_timeout:    15s
  dial_timeout:    10s
  coalesce_window: 2ms
  max_send_chunk:  524288     # 512KB;实际 min(此值, 服务端 max_send_bytes)

health:
  enabled:  true
  mode:     "tcp"             # "tcp" 或 "http"
  path:     "/healthz"        # 仅 mode == "http" 时使用
  interval: 15s
  timeout:  3s

logging:
  level: "info"
```

---

## 十一、错误与状态码

### 公网侧(返回给最终调用方)

| 状态码 | 触发条件 | 产生位置 |
|---|---|---|
| 400 | Host 头非法 / 不属于 `root_domain` / 多层子域名 | `servePublic` |
| 404 | 无匹配的 Host+Path 路由 / 隧道未注册 | `servePublic` / `RouteTable.Match` |
| 502 | 隧道通,但 Client 拨号本地服务失败,或响应读取出错 | `proxyErrorHandler` |
| 503 | 池内无可用后端 / 本地服务不健康(D3)/ 并发流达上限(D6) | `servePublic` |
| 504 | 超过 `response_header_timeout` 仍未收到响应头 | `proxyErrorHandler` |

### 控制面(返回给 Client)

| 状态码 | 触发条件 | Client 的反应 |
|---|---|---|
| 200 | 成功 | 继续 |
| 204 | 长轮询超时无数据 | **正常**,立即重新 poll |
| 400 | 缺 `client_id` / 非法 meta / 请求体读取失败 | 记日志,重连 |
| 401 | token 无效 | **致命** → 传输失败 → 退避重连 |
| 404 | 会话不存在 | **致命** → 传输失败 → 重连 |
| 409 | 该子域名已达 `max_clients`(HttpHop Authenticate) | 记日志,退避重连 |
| **410** | 【A5】服务端主动关闭了会话 | **致命** → 传输失败 → **立即重连** |
| **413** | 【A2】请求体超上限 —— **协议违规** | **致命** → 记录并重连(守规客户端不应出现) |
| **426** | `protocol_version` 不支持 | **致命** → 停止重试,提示升级 |
| **302** 等 3xx | 鉴权失败或误配(反扫描重定向) | **致命** → 检查 auth_token |

---

## 十二、日志与可观测性

### 结构化日志(zap)

**公网请求**:`request_id`、`subdomain`、`client_id`、`remote_addr`、`method`、`path`、`status`、`duration_ms`、`bytes_out`。
**隧道生命周期**(info 级):注册、断开、重连、驱逐,带 `subdomain` / `client_id` / `session_id` / `reason`。
**传输层**(debug 级):poll 往返、发送分片大小、合并命中情况。

**不记录**:请求体、响应体、完整 query string、`Authorization` 头。

### `GET /status`

默认关闭;建议只监听 `127.0.0.1`(见配置)。

```json
{
  "version": "0.1.0",
  "uptime_seconds": 86400,
  "tunnels": [
    {
      "subdomain": "myapp",
      "clients": [
        {
          "client_id": "home-gpu-01",
          "session_id": "9f2c...",
          "remote_addr": "203.0.113.7",
          "connected_at": "2026-08-03T10:00:00Z",
          "last_poll_at": "2026-08-03T10:31:12Z",
          "poll_in_flight": 1,
          "active_streams": 3,
          "local_health": "ok",
          "buffered_to_server": 0,
          "buffered_from_server": 0
        }
      ]
    },
    { "subdomain": "api", "clients": [] }
  ]
}
```

`poll_in_flight`、`last_poll_at`、`local_health` 三项合起来就是需求 #3 的运维出口:一眼能看出某条路径是否还能服务、以及是哪一层出了问题。

---

## 十三、包结构与文件清单

```
HttpHop/
├── go.mod                          # module github.com/DiamondGo/HttpHop, go 1.21
├── README.md                       # 含"上下行吞吐不对称"的说明(B1)
├── Makefile                        # build / test / lint
├── cmd/
│   ├── server/main.go              # 读配置、建 Server、信号处理、优雅停机
│   └── client/main.go              # 读配置、建 Client、信号处理
├── internal/
│   ├── registry/
│   │   ├── registry.go             #   Registry、ClientTunnel
│   │   └── pool.go                 #   ★F TunnelPool + Balancer + firstAvailable
│   ├── router/
│   │   ├── host.go                 #   HostKey + autocert HostPolicy
│   │   └── route.go                #   ★G RouteTable、StripPathPrefix
│   ├── server/
│   │   ├── server.go               #   pollmux 挂载、Hooks、StartSweeper、/status
│   │   ├── proxy.go                #   ★C ReverseProxy + servePublic + DialContext
│   │   ├── bridge.go               #   ★C OpenStream / Bridge(为裸 TCP 预留)
│   │   ├── auth.go                 #   TokenStore + AuthMiddleware
│   │   ├── tls.go                  #   autocert + :80 挑战与跳转 + 证书预热
│   │   └── errors.go               #   writeHTTPError / writeJSONError
│   ├── client/
│   │   ├── client.go               #   pollmux ReconnectLoop + AcceptLoop
│   │   ├── handler.go              #   拨号本地服务 + 双向桥接 + host 改写
│   │   └── health.go               #   ★D3 本地服务健康检查
│   └── config/
│       ├── config.go               #   viper 加载 + 默认值 + pollmux.ServerConfig 映射
│       └── validate.go             #   启动时校验(尤其 session_timeout ≥ 2×poll_timeout)
├── configs/
│   ├── server.example.yaml
│   └── client.example.yaml
├── plans/
│   └── IMPLEMENTATION.md           # ★ 分步骤实现指南
└── test/
    └── integration_test.go         # 进程内端到端
```

**★ 标记的是最关键的文件**:`internal/server/proxy.go`(HttpHop 特有)、`internal/registry/pool.go`(LB 结构预留)。传输层由 **`github.com/DiamondGo/pollmux`** 外部依赖提供,不在本仓库内。

---

## 十四、依赖

| 用途 | 选择 | 说明 |
|---|---|---|
| **长轮询传输 + yamux 胶水** | **`github.com/DiamondGo/pollmux`** | A1–A5、B1 第一层、D3/D10、Limits 下发、Connect/Poll/Delete handler、ReconnectLoop/AcceptLoop |
| 多路复用 | `github.com/hashicorp/yamux`(pollmux 间接依赖) | 通过 `pollmux.ClientSession`/`ServerSession` 建会话,勿直接调 |
| 公网反向代理 | 标准库 `net/http/httputil` | §3.C |
| 控制面路由 | `github.com/gorilla/mux` | 挂载 pollmux handler |
| 配置 | `github.com/spf13/viper` + `mitchellh/mapstructure` | 与 HttpBroker 一致 |
| 日志 | `go.uber.org/zap` + `go.uber.org/zap/exp/zapslog` | HttpHop 用 zap;边界桥接到 pollmux 的 slog |
| 自动 TLS | `golang.org/x/crypto/acme/autocert` | HTTP-01 逐域名签发(不支持通配符) |
| 测试 | 标准库 `testing` + `net/http/httptest` | 传输层回归由 pollmux 测试覆盖;HttpHop 写集成测试 |

---

## 十五、分阶段实现顺序与验收

**逐步任务清单、每步产出与命令见 [plans/IMPLEMENTATION.md](plans/IMPLEMENTATION.md)。** 本节仅保留阶段概览。

每一阶段都要能独立验证再进入下一阶段。**pollmux 库本身已完成并有单测**;HttpHop 从阶段 1 起直接引用。

### 阶段 0:项目骨架与仓库初始化

1. `go mod init github.com/DiamondGo/HttpHop`(Go ≥ 1.21)。
2. `go get github.com/DiamondGo/pollmux`(开发期可用 `replace` 指本地 `../pollmux`)。
3. 建目录骨架;`.gitignore`;`Makefile`(build / test / lint)。
4. `README.md`:项目定位 + 快速上手占位 + **§18 已知限制**。
5. 本设计文档保留在仓库根目录 `ARCHITECTURE.md`(或与 HttpBroker 一致复制到 `plans/architecture.md`)。

**验收**:`go build ./...` 通过(空实现)。

### 阶段 1:配置 + pollmux 接线骨架

`internal/config/{config.go,validate.go}` + 两个 example yaml;`internal/server/server.go` 最小化挂载 pollmux handler(可先用 stub Hooks)。
**验收**:能加载示例配置;`session_timeout < 2×poll_timeout` 时报明确错误;`httptest` 走完 connect → poll → delete(pollmux handler 已挂载)。

### 阶段 2:注册表与路由

`internal/registry/{registry.go,pool.go}`、`internal/router/{host.go,route.go}`。
**验收**:单测 —— HostKey(含 apex `@`);RouteTable 最长前缀与兜底;`strip_prefix` 改写;路径前缀冲突启动失败;同 `client_id` 重连替换。

### 阶段 3:服务端控制面 Hooks ★

完善 `authenticateConnect` / `onConnect` / `onPoll` / `onDisconnect`;`StartSweeper`;证书预热。
**验收**:connect 返回 `limits` + `meta.subdomain`;EOF/CloseSession 时 poll 返回 410;`PollInFlight` 在 `/status` 可见;`X-Local-Health` 更新 Registry。

### 阶段 4:服务端公网侧 ★ HttpHop 特有

`internal/server/{proxy.go,bridge.go}`。
**验收**:`httptest` + 假 yamux Client 端,断言 `X-Forwarded-For` 注入、伪造 XFF 剥离、404/503 路径正确。

### 阶段 5:客户端

`internal/client/{client.go,handler.go,health.go}` —— `ReconnectLoop` + `AcceptLoop`。
**验收**:连上阶段 3 服务端并保持;杀服务端后退避重连;健康状态反映到 `/status`。

### 阶段 6:main 与 TLS

`cmd/server/main.go`、`cmd/client/main.go`、`internal/server/tls.go`。
**验收**:两二进制用示例配置跑通;本地自签证书场景完整请求一次。

### 阶段 7:集成测试与文档

`test/integration_test.go`;README(**必须写明上下行吞吐不对称**);部署说明。
**验收**:§16 全部用例通过。

---

## 十六、测试计划

### 单元测试

| 目标 | 关键用例 |
|---|---|
| pollmux(外部) | A1/A2/A5/B1 等已由库单测覆盖;HttpHop 不重复 |
| `Registry`/`Pool` | 同 ID 替换;`max_clients` 上限;并发注册无 race |
| `router.HostKey` / `RouteTable` | apex `@`;最长前缀;兜底 `path_prefix:""`;StripPathPrefix 边界 |
| `config.Validate` | 各项约束尤其 `session_timeout ≥ 2×poll_timeout` |
| Server Hooks | Authenticate 409;OnConnect 登记;OnPoll 健康上报 |

### 集成测试(`test/integration_test.go`,进程内)

起 server + client + 本地 echo 服务,全部在同一进程内用随机端口:

1. **基本转发**:请求经隧道到达 echo 服务,响应正确。
2. **路径映射**:公网 `builderrors.com/service/auth` + `strip_prefix: true` → 内网 echo 收到 `/auth`。
3. **多租户路由**:两个 client / 两个 token / 不同 Host 或 PathPrefix,互不串路。
4. **并发**:50 个并发请求不串行化(总耗时应接近单个请求而非 50 倍)—— 验证 yamux 多路复用 + 读写分离确实消除了队头阻塞。
4. **重连**:关掉 server 再起来,client 自动重连,期间的请求返回 503/504 而非挂死。
5. **410 快速重连**(A5):server 主动关闭会话,断言 client 在**秒级**内重连,而不是等到 `session_timeout`。
6. **Limits 下发**:客户端 `max_send_chunk` 大于服务端 `max_send_bytes` 时,断言实际使用服务端值;`poll_interval` 过大时 connect 后自检失败。
7. **413 不断链语义变更**(A2):守规客户端不应触发 413;若手工构造超限请求,断言会话关闭而非减半重试。
8. **X-Forwarded-For**(D1):echo 服务回显请求头,断言拿到真实调用方 IP;再从公网侧伪造 `X-Forwarded-For: 1.2.3.4`,断言它被**剥离**而非被信任。
9. **本地服务健康**(D3):停掉 echo 服务,断言 `/status` 在 `interval` 内把 `local_health` 置为 `down`,且该子域名开始返回 503。
10. **LB 结构未退化**(F):`max_clients: 1` 时第二个 client 注册返回 409;同 `client_id` 重连是替换;重连后 `ClientTunnel.ID` 不变。
11. **protocol_version**:客户端声明 `999`,断言服务端 426 且停止重试。

### 手工/端到端验证

1. **心跳三层验收(需求 #3 的核心)**:
    - a. `kill -9` 客户端 → 服务端应在 60s 内驱逐;若有 TCP FIN,`poll_in_flight` 应**立即**归零。
    - b. `iptables -A OUTPUT -d <server> -j DROP` 模拟静默黑洞 → **客户端**应在 ~40s 内检测到并进入重连(**A1 的直接验收**)。
    - c. 只停本地服务、保持隧道 → `/status` 应在 15s 内标记 `local_health: down`(**D3 的直接验收**)。
2. **吞吐**:`tc netem` 注入 100ms RTT,下载 100MB 文件;对比 `poll_buffer_size` 64KB vs 256KB,预期 ×4(**B1 第一层的量化验收**,同时作为将来做流式 poll 的性能基线)。
3. **WebSocket**:通过隧道建立 WS 连接并双向收发若干消息。
4. **SSE**:通过隧道消费一个 SSE 流,断言事件**逐条实时到达**而非被缓冲(验证 `FlushInterval: -1`)。
5. **HTTP/2**:`curl --http2` 正常工作(这是 §3.C 相对裸桥接方案的直接收益)。
6. **大请求体**:POST 5MB body 成功且隧道不重置。
7. **证书签发**:真实域名下走一次 autocert HTTP-01,确认 `HostPolicy` 只放行已注册子域名。

---

## 十七、MVP 范围与推后项

MVP = **第一个能真正部署、能把内网服务可靠暴露到公网的版本**,不是功能齐全的版本。划线依据是"缺了它交付物就是坏的",而非功能大小。

### MVP 必须包含

**因为它们是缺陷不是功能**(推后等于交付一个已知会断的东西):A1、A2、A3、A4、A5。

**因为它们是产品定义的一部分**:

- 引用 **pollmux** 作为传输层(§5)
- **域名映射 + 路径映射**(§3.G):含 apex `@`、`path_prefix`、`strip_prefix`
- ReverseProxy 公网侧 + `bridge.go` 抽取(§3.C)
- 多租户注册表与 Host 路由,含 **§3.F 的结构预留**(`TunnelPool` / `client_id` / `Balancer`)
- token → 子域名绑定认证
- autocert + :80 挑战 + 单端口 Host 分流 + 证书预热
- D3 本地服务健康检查
- D9 `/status`
- B1 第一层:`poll_buffer_size` 可配,默认 256KB

### 明确推后

| 项 | 推后理由 |
|---|---|
| **B1 第二层:流式 chunked poll** | 要改 pollmux 的 `pollLoop`/`PollHandler`,应等 MVP 跑通后再动;且需实测前置 CDN 对 chunked 的缓冲行为。`poll_mode` 配置项已预留 |
| LB 策略(轮询/最少连接/一致性哈希) | MVP 只留 `Balancer` 接口 + `firstAvailable` |
| sticky cookie 读写、后端健康剔除、故障重试 | 依赖 LB 策略 |
| 裸 TCP/UDP 端口转发 | `bridge.go` 已预留复用点 |
| unauthorized_redirect 反扫描模式 | 移植自 HttpBroker,非必需 |
| Web 管理面板、限速、WAF、DNS-01 通配符证书、双向 TLS、单 Client 多本地目标 | 不影响可用性 |

**MVP 之后的优先级**:① B1 第二层流式 poll(下行吞吐的质变)→ ② LB 策略 + sticky session → ③ 其余。

---

## 十八、已知限制

实现完成后应写进 README,避免使用者踩坑:

1. **上下行吞吐不对称**:下行受 `poll_buffer_size / RTT` 限制(256KB @ 150ms ≈ 1.7MB/s),上行受 `max_send_chunk / RTT` 限制但缓冲自适应,实际更高。跨境高 RTT 场景下行是瓶颈,等流式 poll 落地后解除。
2. **单层子域名 + apex 根域**:`a.b.builderrors.com` 不支持;`builderrors.com` 通过 `subdomain: "@"` 支持。
3. **路径映射**仅支持前缀匹配 + 可选剥前缀;**部署上优先整子域名映射**,apex 路径前缀为备选(见 §G0 / README)。
4. **响应已开始后无法降级**:响应头已发出再断连,只能中断连接,无法改成 502 —— 这是 HTTP 的固有限制,不是本设计的缺陷。
5. **前置 CDN/反向代理需要额外配置**:长轮询会被某些默认配置(如 nginx 的 `proxy_read_timeout 60s`)截断;`poll_timeout` 需相应调小。HttpHop **不依赖**前置反代做路径路由,但若 CDN 仍在前端,需单独调 CDN 超时。
6. **`session_timeout` 与 `poll_timeout` 强耦合**:服务端配置必须满足 `session_timeout ≥ 2×poll_timeout`;connect 下发的 `limits` 保证客户端与服务端一致。
7. **内存**:yamux 窗口下限 256KB,典型并发下约 **64MB/隧道**;多租户需乘以隧道数,可用 `max_streams_per_tunnel` 下调。
8. **本地健康检查只覆盖"能否连通/返回 2xx"**,不代表业务层健康。
9. **单 Client 只能暴露一个本地 target**:多目标需要多开 Client 实例,或在同一 target 后自建应用层路由。
