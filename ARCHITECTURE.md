# HttpHop 详细设计文档(v4)

> **文档状态**:设计定稿,尚未开始实现。仓库 `/home/kexie/source/HttpHop` 目前为空。
> **本文目标**:详细到可以直接照着写代码 —— 包含结构体定义、函数签名、协议规范、关键逻辑伪代码、配置样例、分阶段实现顺序与验收标准。

---

## 目录

1. [背景与目标](#一背景与目标)
2. [已确定的决策](#二已确定的决策)
3. [设计评审:发现的问题与改进](#三设计评审发现的问题与改进)
4. [总体架构](#四总体架构)
5. [传输层详细设计](#五传输层详细设计)
6. [隧道控制协议规范](#六隧道控制协议规范)
7. [心跳与失效检测](#七心跳与失效检测)
8. [服务端详细设计](#八服务端详细设计)
9. [客户端详细设计](#九客户端详细设计)
10. [配置](#十配置)
11. [错误与状态码](#十一错误与状态码)
12. [日志与可观测性](#十二日志与可观测性)
13. [包结构与文件清单](#十三包结构与文件清单)
14. [依赖](#十四依赖)
15. [分阶段实现顺序与验收](#十五分阶段实现顺序与验收)
16. [测试计划](#十六测试计划)
17. [MVP 范围与推后项](#十七mvp-范围与推后项)
18. [已知限制](#十八已知限制)

---

## 一、背景与目标

在空仓库 `/home/kexie/source/HttpHop` 中从零构建 **HttpHop**:

- **Server(服务端)**:部署在有公网 IP 和域名、但本地资源很少的机器上。对外提供 HTTPS 服务,把收到的公网 HTTP 请求转发给已连接的 Client。
- **Client(客户端)**:部署在内网(无法被外部主动连接)、但本地算力/服务很强的机器上。接收 Server 转发过来的请求,转发到本地可访问的 HTTP 服务,再把响应原路返回。

典型场景:把内网的强力 HTTP 服务通过一台轻量公网机器暴露到互联网,不需要端口映射,也不暴露内网。

**参考项目**:`~/source/HttpBroker` 是同一作者的兄弟项目,实现了 SOCKS5-over-HTTP 隧道。它的 `internal/transport` 包已经把"长轮询作为虚拟连接"这件事做得相当完善,HttpHop 直接移植复用(并修掉本文第三节列出的缺陷)。

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
| 2 | Server 支持**多个 Client 同时接入**,按**子域名**路由 | 用户明确要求 |
| 3 | 传输层**必须用长轮询**,并**把长轮询本身当作心跳** | 用户明确要求:服务端要**尽早**知道某条路径不能服务了 |
| 4 | 长轮询的缺点参照 `~/source/HttpBroker` 的做法解决 | 用户明确指定 |
| 5 | 公网侧用 **`httputil.ReverseProxy` + 自定义 `DialContext`**,并把桥接逻辑抽进 `bridge.go` 为裸 TCP 预留 | 见 §3.C。裸桥接方案会被 HTTP/2 打崩、拿不到真实客户端 IP、且**无法支撑后续的会话保持需求** |
| 6 | 后续要做**负载均衡 + 会话保持**;MVP **只预留结构、不实现策略** | 见 §3.F。预留成本约几十行,不预留将来要同时改三处 |
| 7 | 下行吞吐 MVP **只做"加大 poll 缓冲"这一层**;流式 chunked poll 推到 MVP 之后 | 见 §3.B。地基要先跑稳再优化 |

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
  **理论依据**:健康的服务端在**最迟 `poll_timeout` 时刻一定会返回 204**(见 §6 的 `handlePoll` 契约),所以超过 `poll_timeout + 宽限` 还没收到响应头,就是确定性的链路故障证据。
- `Transport.DialContext` 用 `&net.Dialer{Timeout: 10s, KeepAlive: 15s}`,开启 TCP keepalive。
- **发送请求用独立的、更短超时的 `sendClient`**(`Timeout: 15s`)—— 发送不该被长轮询的宽松超时约束。
- 结果:**空闲期最坏检测延迟 ≈ 40s;有流量时 ≈ 15s**。

#### A2. 服务端 1MB 体上限 vs 客户端无上限写缓冲 → 隧道被整条重置

**证据**:`HttpBroker/internal/broker/server.go:252` 是 `http.MaxBytesReader(w, r.Body, 1<<20)`,而 `httpconn.go:172-180` 的 `flushLoop` 把**整个无上限的 `writeBuf` 一次性发出**。

**问题**:yamux 每流默认 256KB 窗口,并发 10 条流在途数据就可能超过 1MB → 服务端 400 → `doSend` 看到非 200/204 → `signalTransportFailed()` → **整条隧道断开重连,所有在途请求全挂**。

**修复**:

- `flushLoop` 每次最多取 `max_send_chunk`(默认 512KB)。
- 服务端 `MaxBytesReader` 上限 = `max_send_chunk × 2`(1MB),两边写进注释互相引用。
- 服务端超限返回 **413**(不是 400),客户端把 413 当作**可恢复错误**:把 `max_send_chunk` 减半后重试,而不是判定传输失败。

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

**修复**(靠上层流控兜底,而不是给 pipe 加阻塞语义 —— 后者会引入死锁风险):

- `yamux.Config.MaxStreamWindowSize` 从默认 256KB 降到 **64KB**(隧道的实际带宽时延积远小于此)。
- 每隧道最大并发流 **256**,超出直接返回 503。
- 上限内存 ≈ 256 × 64KB = **16MB / 隧道**。
- `BufferedPipe` 增加 `HighWaterMark`,超过时打 warn 日志(仅告警,不阻塞)。

#### A5. 服务端关闭会话时,客户端不会及时重连(新发现)

**证据**:`server.go:298-302` —— `ReadAvailable` 返回 `io.EOF`(pipe 已关闭)时,服务端返回 **204**。而 `httpconn.go:311-313` 里客户端把 204 当作"无数据,继续轮询"。

**问题**:服务端主动关闭会话(优雅停机、yamux 会话死亡、管理员踢下线)后,客户端会**一直空转轮询**,直到 `session_timeout` 把会话从注册表里清掉、客户端才收到 404。这期间隧道实际已经不可用,但客户端不知道要重连。

**修复**:`handlePoll` 在 `err == io.EOF` 时返回 **410 Gone**(而不是 204);客户端把 410 与 404/401 同等对待 —— 触发 `signalTransportFailed()` 立即进入重连。

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

**推后理由**:(a) 要重写 `pollLoop` 和 `handlePoll` 两个地基函数,应等 MVP 跑通、传输层有测试覆盖后再动;(b) 前置 CDN/nginx 的某些配置会缓冲 chunked 响应导致该模式直接失效,需要实测。
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

- 移植 HttpBroker 的 `pipe_test.go` / `httpconn_test.go` 作为传输层单测基础。
- 新增端到端集成测试:进程内起 server + client + 本地 echo 服务。
- 模块路径 `github.com/DiamondGo/HttpHop`(与 HttpBroker 一致)。Go ≥ 1.21。

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
    │          │      POST   /tunnel/connect      客户端注册        │
    │          │      POST   /tunnel/{id}/poll    长轮询(收)/立即发 │
    │          │      DELETE /tunnel/{id}         干净下线          │
    │          │      GET    /status              健康观测          │
    │          │                                                   │
    │          └─ 其他 Host ─→ servePublic()                        │
    │                  │ 1. router.Subdomain(Host) 解析子域名       │
    │                  │ 2. Registry.Pool(sub) 查池                 │
    │                  │ 3. pool.Pick(r) 选后端(MVP: firstAvail)   │
    │                  │ 4. 健康 / 并发上限检查                      │
    │                  │ 5. 塞进 context,交给 ReverseProxy         │
    │                  ▼                                           │
    │           httputil.ReverseProxy                               │
    │             Rewrite:  SetXForwarded + 保持原 Host             │
    │             Transport.DialContext → bridge.OpenStream()       │
    │             FlushInterval: -1(SSE 实时)                      │
    │             ErrorHandler → 502 / 503 / 504                    │
    │                  │                                           │
    │                  ▼  yamux.Open()(Server 是 yamux.Client)     │
    │           Registry map[subdomain]*TunnelPool                  │
    └──────────────────────────────────────────────────────────────┘
                                   │
              虚拟连接 = 滚动的 HTTP 长轮询循环
       Client 侧 HTTPConn  ←──────────────────→  Server 侧 Session
       (pollLoop + flushLoop)                  (ToServer/FromServer 两个 BufferedPipe)
                                   │
    ┌──────────────────────────────────────────────────────────────┐
    │                          CLIENT                              │
    │  HTTPConn(pollLoop 长轮询收 + flushLoop 合并写)               │
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

```
公网调用方
  │ ① HTTPS 请求  Host: myapp.httphop.io
  ▼
Server :443  (http.Server 解析请求)
  │ ② servePublic:解析子域名 "myapp" → Registry 查池 → Pick 后端
  │ ③ 塞 tunnel 进 context,proxy.ServeHTTP
  ▼
ReverseProxy.Rewrite  (注入 X-Forwarded-For 等,保持原 Host)
  │ ④ Transport.RoundTrip → DialContext
  ▼
bridge.OpenStream(tunnel)  → yamux.Session.Open()  返回一条流(net.Conn)
  │ ⑤ Go 标准库把 HTTP 请求写进这条流
  ▼
yamux 把流数据切成帧 → 写进 Session.FromServer (BufferedPipe)
  │ ⑥ 客户端挂起的长轮询 poll 请求被唤醒,把数据作为 200 响应体返回
  ▼
Client HTTPConn.readPipe → yamux.Server 解帧 → Accept() 到这条流
  │ ⑦ StreamHandler.Handle:DialTimeout 本地服务
  ▼
本地 HTTP 服务处理请求,返回响应
  │ ⑧ io.Copy 回 yamux 流 → HTTPConn.Write → flushLoop 合并 → X-Send-Only POST
  ▼
Server handlePoll 把请求体写进 Session.ToServer → yamux 解帧
  │ ⑨ Go 标准库 http.ReadResponse 从流上读出响应
  ▼
ReverseProxy 把响应写回公网调用方(FlushInterval: -1,逐块实时)
```

---

## 五、传输层详细设计

包 `internal/transport`,从 `~/source/HttpBroker/internal/transport` 移植并修改。**这是整个项目的地基,必须先做对。**

### 5.1 `pipe.go` —— `BufferedPipe`

线程安全的内存管道,用 `sync.Mutex` + `sync.Cond`。**基本原样移植**,仅加 A4 的高水位告警。

```go
type BufferedPipe struct {
    mu     sync.Mutex
    cond   *sync.Cond
    buf    []byte
    closed bool

    // A4:超过此值打 warn 日志(仅告警,不阻塞 —— 加阻塞语义会引入死锁风险)
    highWaterMark int
    warned        bool
    onHighWater   func(n int)
}

const DefaultCoalesceWindow = 2 * time.Millisecond

func NewBufferedPipe() *BufferedPipe
func (p *BufferedPipe) Write(data []byte) (int, error)   // 追加 + cond.Signal
func (p *BufferedPipe) Read(dst []byte) (int, error)     // 阻塞直到有数据或关闭
func (p *BufferedPipe) Close() error                     // cond.Broadcast 唤醒所有等待者
func (p *BufferedPipe) Buffered() int                    // 新增:供 /status 观测

// ReadAvailable 是长轮询的核心原语,两阶段:
//   阶段一:若 buf 空,最长等 timeout 等第一个字节(这是真正的长轮询等待)
//   阶段二:拿到第一个字节后,再等一个很短的 coalesceWindow 攒更多数据(上限 len(dst))
// 返回 (0, nil) 表示纯超时无数据(调用方应回 204);返回 io.EOF 表示已关闭且空。
func (p *BufferedPipe) ReadAvailable(dst []byte, timeout, coalesceWindow time.Duration) (int, error)
```

**为什么需要阶段二**:没有它的话,只要有 1 个字节落进 buf,poll 响应就立刻发出;而调用方(`pollLoop`)收到数据后会立即重新 poll —— 结果每一点点数据都要一次完整往返,`dst` 的容量永远用不满。阶段二是下行方向的写合并,与 `HTTPConn.Write` 的上行合并是同一类修复。

### 5.2 `session.go` —— `Session`(服务端侧虚拟连接)

```go
type Session struct {
    ID        string        // 随机 32 字符 hex,每次连接都不同
    ClientID  string        // 【F】跨重连稳定的标识,来自 Client 配置
    Subdomain string        // 由 token 决定,Client 不能自选

    ToServer   *BufferedPipe // Client → Server 方向(handlePoll 写入,yamux 读出)
    FromServer *BufferedPipe // Server → Client 方向(yamux 写入,handlePoll 读出)

    LastActive   time.Time
    pollInFlight int32      // 【A3】原子计数:>0 表示当前有 poll 挂在服务端

    mu     sync.Mutex
    closed bool
}

func NewSession(id, clientID, subdomain string) *Session

// io.ReadWriteCloser —— 供 yamux 直接使用
func (s *Session) Read(p []byte) (int, error)  { return s.ToServer.Read(p) }
func (s *Session) Write(p []byte) (int, error) { return s.FromServer.Write(p) }
func (s *Session) Close() error                 // 关闭两个 pipe,幂等

func (s *Session) Touch()
func (s *Session) IsExpired(timeout time.Duration) bool

// 【A3】poll 生命周期
func (s *Session) BeginPoll()          // atomic.AddInt32(&pollInFlight, 1); Touch()
func (s *Session) EndPoll()            // atomic.AddInt32(&pollInFlight, -1); Touch()
func (s *Session) PollInFlight() int32
```

> **注意命名**:HttpBroker 里叫 `ToBroker`/`FromBroker`,这里改名为 `ToServer`/`FromServer`。方向语义不变。

### 5.3 `httpconn.go` —— `HTTPConn`(客户端侧虚拟连接)

这是改动最多的文件。移植 + A1 + A2 + A5 + D3 + D10。

```go
type HTTPConn struct {
    sessionID string
    pollURL   string        // {server}/tunnel/{id}/poll
    deleteURL string        // {server}/tunnel/{id}
    authToken string

    // 【A1】两个独立的 http.Client
    pollClient *http.Client // ResponseHeaderTimeout = pollTimeout + grace;整体 Timeout = 0
    sendClient *http.Client // 整体 Timeout = sendTimeout(15s)

    readPipe *BufferedPipe

    transportFailedCh chan struct{}
    transportFailOnce sync.Once

    closed int32
    stopCh chan struct{}
    wg     sync.WaitGroup

    // 写侧合并
    writeMu             sync.Mutex
    writeBuf            []byte
    writeFlightOn       bool
    writeCoalesceWindow time.Duration
    maxSendChunk        int  // 【A2】单次发送上限,默认 512KB;遇 413 时减半

    // 【D3】本地健康状态,由 client/health.go 提供,pollLoop 每次搭车上报
    localHealth func() bool

    logger *zap.Logger
}
```

#### `Write` / `flushLoop`(A2 修复)

```go
func (c *HTTPConn) Write(p []byte) (int, error) {
    if atomic.LoadInt32(&c.closed) == 1 { return 0, io.ErrClosedPipe }
    if len(p) == 0 { return 0, nil }

    c.writeMu.Lock()
    c.writeBuf = append(c.writeBuf, p...)
    if c.writeFlightOn {           // 已有 flushLoop 在跑,排队即可
        c.writeMu.Unlock()
        return len(p), nil
    }
    c.writeFlightOn = true
    c.writeMu.Unlock()

    c.wg.Add(1)
    go c.flushLoop()
    return len(p), nil
}
```

> `Write` 返回即代表"已排队",不代表"对端已收到" —— 与真实 `net.Conn` 的语义一致。发送失败通过 `TransportFailed()` 异步上报。

```go
func (c *HTTPConn) flushLoop() {
    defer c.wg.Done()
    first := true
    for {
        if first {
            // 一次突发只付一次合并窗口的代价:让紧随其后的 Write(最典型的是
            // yamux 同一帧的 header + body 两次调用)有机会并进同一个 HTTP 请求。
            time.Sleep(c.writeCoalesceWindow)
            first = false
        }

        c.writeMu.Lock()
        if len(c.writeBuf) == 0 {
            c.writeFlightOn = false
            c.writeMu.Unlock()
            return
        }
        // 【A2】分片:每次最多取 maxSendChunk
        n := min(len(c.writeBuf), c.maxSendChunk)
        chunk := make([]byte, n)          // 必须复制:直接切片会让整个底层数组
        copy(chunk, c.writeBuf[:n])       // 无法回收,长连接下是内存泄漏
        c.writeBuf = append(c.writeBuf[:0], c.writeBuf[n:]...)
        c.writeMu.Unlock()

        if err := c.doSend(chunk); err != nil {
            if errors.Is(err, errChunkTooLarge) {   // 413:可恢复
                c.writeMu.Lock()
                c.maxSendChunk = max(c.maxSendChunk/2, minSendChunk)  // 减半后重试
                c.writeBuf = append(chunk, c.writeBuf...)             // 放回队首
                c.writeMu.Unlock()
                continue
            }
            c.logger.Error("send failed, signalling transport failure", zap.Error(err))
            c.signalTransportFailed()
            c.readPipe.Close()
            c.writeMu.Lock(); c.writeFlightOn = false; c.writeMu.Unlock()
            return
        }
    }
}
```

#### `doSend`

```go
func (c *HTTPConn) doSend(buf []byte) error {
    req, _ := http.NewRequest(http.MethodPost, c.pollURL, bytes.NewReader(buf))
    req.Header.Set("Content-Type", "application/octet-stream")
    req.Header.Set("X-Send-Only", "true")
    if c.authToken != "" { req.Header.Set("Authorization", "Bearer "+c.authToken) }

    resp, err := c.sendClient.Do(req)     // 【A1】短超时的 client
    if err != nil { return fmt.Errorf("send: %w", err) }
    defer resp.Body.Close()
    io.Copy(io.Discard, resp.Body)

    switch resp.StatusCode {
    case http.StatusOK, http.StatusNoContent:
        return nil
    case http.StatusRequestEntityTooLarge:   // 413
        return errChunkTooLarge              // 【A2】可恢复
    default:
        return fmt.Errorf("server returned status %d", resp.StatusCode)
    }
}
```

#### `pollLoop`(A1 + A5 + D3 + D10)

```go
func (c *HTTPConn) pollLoop(pollInterval time.Duration) {
    defer c.wg.Done()

    for {
        select {
        case <-c.stopCh: return
        default:
        }

        req, _ := http.NewRequest(http.MethodPost, c.pollURL, nil)
        req.Header.Set("X-Receive-Only", "true")
        if c.authToken != "" { req.Header.Set("Authorization", "Bearer "+c.authToken) }
        // 【D3】健康状态搭车上报 —— 不需要额外的控制流
        if c.localHealth != nil {
            req.Header.Set("X-Local-Health", boolToHealth(c.localHealth()))
        }

        resp, err := c.pollClient.Do(req)   // 【A1】ResponseHeaderTimeout = pollTimeout+grace
        if err != nil {
            if atomic.LoadInt32(&c.closed) == 1 { return }
            // 超时也走这里 —— 这正是 A1 想要的:静默黑洞在 ~40s 内被发现
            c.logger.Warn("poll failed, signalling transport failure", zap.Error(err))
            c.signalTransportFailed()
            c.readPipe.Close()
            return
        }

        gotData := false
        switch resp.StatusCode {
        case http.StatusOK:
            data, err := io.ReadAll(resp.Body); resp.Body.Close()
            if err != nil { continue }
            if len(data) > 0 {
                gotData = true
                if _, err := c.readPipe.Write(data); err != nil { return }
            }
        case http.StatusNoContent:           // 204:健康的空闲超时
            resp.Body.Close()
        case http.StatusNotFound,            // 404:会话不存在(服务端重启/已清理)
             http.StatusUnauthorized,        // 401:鉴权失败
             http.StatusGone,                // 410:【A5】服务端主动关闭了会话
             http.StatusFound:               // 302:通常是鉴权失败或误配
            resp.Body.Close()
            c.logger.Warn("session invalid", zap.Int("status", resp.StatusCode))
            c.signalTransportFailed()
            c.readPipe.Close()
            return
        default:
            resp.Body.Close()
            c.logger.Warn("unexpected status", zap.Int("status", resp.StatusCode))
        }

        // 【D10】pollInterval 默认 0:收到数据后立即重 poll,把等待留给服务端的长轮询
        if !gotData && pollInterval > 0 {
            if !c.sleepOrStop(pollInterval) { return }
        }
    }
}
```

#### 其余方法

```go
func (c *HTTPConn) Read(p []byte) (int, error) { return c.readPipe.Read(p) }
func (c *HTTPConn) TransportFailed() <-chan struct{}   // 传输级失败信号
func (c *HTTPConn) Close() error                        // 停 poll、等 goroutine、DELETE 会话
```

### 5.4 `transport.go` —— 连接器

```go
type Conn interface {
    io.ReadWriteCloser
    TransportFailed() <-chan struct{}
}

type HTTPConnector struct {
    PollInterval        time.Duration
    PollTimeout         time.Duration   // 用于推导 ResponseHeaderTimeout
    PollGrace           time.Duration   // 【A1】默认 10s
    SendTimeout         time.Duration   // 【A1】默认 15s
    WriteCoalesceWindow time.Duration
    MaxSendChunk        int             // 【A2】默认 512KB
    AuthToken           string
    ClientID            string          // 【F】
    InsecureSkipVerify  bool
    LocalHealth         func() bool     // 【D3】
    Logger              *zap.Logger
}

// Connect 发起 POST /tunnel/connect,拿到 session_id 后构造 HTTPConn 并启动 pollLoop。
func (c *HTTPConnector) Connect(serverURL string) (Conn, error)
```

### 5.5 yamux 配置(两端共用的辅助函数)

```go
func YamuxConfig() *yamux.Config {
    cfg := yamux.DefaultConfig()
    cfg.LogOutput = io.Discard

    // 【关键坑】必须关掉 yamux 自带的 keepalive。长轮询挂起时,PING 无法在 yamux 的
    // ConnectionWriteTimeout(10s)内完成往返,会产生假的"连接已死"信号。
    // 存活性完全由 pollLoop 承担(见 §7),不由 yamux 承担。
    cfg.EnableKeepAlive = false

    // 【A4】内存上限:256 流 × 64KB = 16MB / 隧道
    cfg.MaxStreamWindowSize = 64 * 1024
    return cfg
}
```

**角色分配**:

- **Server = `yamux.Client`** —— 主动 `Open()` 流,把每个公网请求推给 Client。
- **Client = `yamux.Server`** —— `Accept()` 流。

与 HttpBroker 处理 provider 的方式完全一致(`relay.go` 的 `HandleProvider`)。

---

## 六、隧道控制协议规范

所有端点都在**控制域名**下(如 `https://tunnel.example.com`),都要求 `Authorization: Bearer <token>`。

### 6.1 `POST /tunnel/connect`

Client 注册,建立一条隧道。

**请求**

```
POST /tunnel/connect HTTP/1.1
Host: tunnel.example.com
Authorization: Bearer <token>
Content-Type: application/json

{"client_id": "home-gpu-01", "version": "0.1.0"}
```

**响应 200**

```json
{"session_id": "9f2c...e41a", "subdomain": "myapp", "poll_timeout": "30s"}
```

> `subdomain` 由服务端根据 token 决定并回传(Client 只是知情,不能自选);`poll_timeout` 回传是为了让 Client 自己算出正确的 `ResponseHeaderTimeout`,避免两边配置漂移。

**错误**:401(token 无效)、400(缺 `client_id`)、409(该子域名已达 `max_clients_per_subdomain`)。

**服务端处理顺序**(顺序很重要):

```
1. 验证 token → 查出绑定的 subdomain
2. 校验 client_id 非空
3. 生成 session_id(crypto/rand 16 字节 hex)
4. session := transport.NewSession(sessionID, clientID, subdomain)
5. 【竞态修复】先把 session 注册进 registry,再启动 yamux goroutine。
   HttpBroker 明确修过这个 bug(server.go:208-213):否则第一个 poll
   可能在 goroutine 建好 yamux 之前到达,拿到 404,导致持续轮询失败。
6. go relay.Handle(session)  →  yamux.Client(session, YamuxConfig())
7. 【F】把 ClientTunnel 加进 TunnelPool(按 client_id 去重:同 ID 视为重连,替换旧的)
8. 【D12】后台预热该子域名的 TLS 证书
9. 返回 200 + JSON
```

### 6.2 `POST /tunnel/{id}/poll`

一个端点承担两种模式,由 header 区分。这是"读写分离"的实现方式,也是队头阻塞被消除的根本原因。

| Header | 模式 | 行为 |
|---|---|---|
| `X-Send-Only: true` | 发送 | 把请求体写入 `ToServer`,**立即**返回 200,不等待 |
| `X-Receive-Only: true` | 接收 | 长轮询 `FromServer`,最长等 `poll_timeout` |

**其他可选 header**:`X-Local-Health: ok | down`(D3,搭车上报本地健康)。

**响应**

| 状态码 | 含义 | Body |
|---|---|---|
| 200 | 有数据(接收模式)/ 发送成功(发送模式) | `application/octet-stream` 数据 / 空 |
| 204 | 长轮询超时,无数据。**正常心跳信号** | 空 |
| 401 | token 无效 | JSON |
| 404 | 会话不存在(服务端重启/已被清理) | JSON |
| **410** | **【A5】服务端主动关闭了会话,Client 应立即重连** | JSON |
| **413** | **【A2】请求体超过上限,Client 应缩小分片重试** | JSON |

**服务端 `handlePoll` 伪代码**:

```go
func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
    sessionID := mux.Vars(r)["id"]
    session, ok := s.registry.GetSession(sessionID)
    if !ok { writeError(w, 404, "session not found"); return }

    session.BeginPoll()          // 【A3】
    defer session.EndPoll()

    // 【D3】搭车上报的健康状态
    if h := r.Header.Get("X-Local-Health"); h != "" {
        s.registry.SetLocalHealth(session.Subdomain, session.ClientID, h == "ok")
    }

    // 【A2】上限 = max_send_chunk × 2
    data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBody))
    if err != nil {
        var mbe *http.MaxBytesError
        if errors.As(err, &mbe) { writeError(w, 413, "request body too large"); return }
        writeError(w, 400, "failed to read body"); return
    }
    if len(data) > 0 { session.ToServer.Write(data) }

    if r.Header.Get("X-Send-Only") == "true" {
        w.WriteHeader(200)       // 发送模式:立即返回,绝不等待
        return
    }

    // 接收模式:长轮询
    buf := s.bufPool.Get().([]byte)          // 【B1】sync.Pool 复用 poll_buffer_size 大小的缓冲
    defer s.bufPool.Put(buf)

    n, err := session.FromServer.ReadAvailable(buf, s.cfg.PollTimeout, s.cfg.CoalesceWindow)
    switch {
    case n > 0:
        w.Header().Set("Content-Type", "application/octet-stream")
        w.WriteHeader(200)
        w.Write(buf[:n])
    case err == io.EOF:
        writeError(w, 410, "session closed")  // 【A5】不是 204!
    default:
        w.WriteHeader(204)                    // 正常的空闲心跳
    }
}
```

### 6.3 `DELETE /tunnel/{id}`

Client 干净下线。关闭 `Session`(两个 pipe 关闭 → yamux 收到 EOF → 会话关闭 → 注册表驱逐)。响应 200 / 404。

### 6.4 `GET /status`

见 §12。可通过配置关闭(默认关闭)。

### 6.5 时序图

**正常请求转发**

```
公网      Server                              Client            本地服务
 │          │                                   │                  │
 │          │◀────── poll(挂起,等 30s) ────────│                  │
 │─请求────▶│                                   │                  │
 │          │ yamux.Open() → FromServer.Write   │                  │
 │          │──── 200 + 数据 ──────────────────▶│                  │
 │          │◀───── poll(立即重发) ────────────│  yamux.Accept()  │
 │          │                                   │──── dial ───────▶│
 │          │                                   │◀─── 响应 ────────│
 │          │◀──── X-Send-Only POST(响应数据) ─│                  │
 │◀─响应────│                                   │                  │
```

**空闲心跳**

```
Client                     Server
  │──── poll ──────────────▶│  ReadAvailable 阻塞 30s
  │                         │
  │◀─────── 204 ────────────│  超时,无数据
  │──── poll(立即) ───────▶│  ← 服务端据此更新 LastActive,确认 Client 存活
```

**Client 掉线的两种情形**

```
情形一(有 TCP FIN/RST,如 kill 进程):
  Server: 挂起的 poll 立即返回错误 → pollInFlight 归零 → 【瞬时感知】
          清理 goroutine(5s 一跳)在 session_timeout(60s)后驱逐

情形二(静默黑洞,如拔网线 / iptables DROP):
  Client: pollClient 在 poll_timeout+grace(40s)后超时
          → signalTransportFailed → 关闭会话 → 退避重连
  Server: 挂起的 poll 一直挂着,直到 TCP 自身超时;
          LastActive 停止更新 → 60s 后被清理 goroutine 驱逐
```

---

## 七、心跳与失效检测

需求 #3 的机制层回答。**三层,各自覆盖不同的失效模式**:

| 层 | 机制 | 检测延迟 | 覆盖的失效 |
|---|---|---|---|
| **1. 客户端自检** | `pollLoop` 本身就是持续的出站探针。任何请求失败(连接拒绝、TLS 失败、非 2xx、**A1 的响应头超时**)都在 poll goroutine 内同步暴露 → `signalTransportFailed()` → 拆会话 → 退避重连 | 快速失败:**秒级**<br>静默黑洞:**≈40s**(A1) | 网络中断、服务端重启、会话失效 |
| **2. 服务端自检** | 每次 poll 更新 `LastActive` 并维护 `pollInFlight`。健康 Client 保证每 `poll_timeout` 至少来一次 poll | 有 TCP FIN/RST:**接近瞬时**(A3 的 `pollInFlight`)<br>其他:**≤60s**(`session_timeout`,5s 扫描粒度) | Client 进程被杀、掉电、单向不通 |
| **3. 本地服务健康** | Client 每 15s 探测本地目标,结果搭车在 `X-Local-Health` 上报;不健康时服务端对该子域名直接返回 503 | **≈15s**(D3) | **隧道通但内网服务本身挂了** |

**为什么这三层缺一不可**:第 1 层保护的是客户端自身(它需要知道何时重连);第 2 层保护的是服务端(它需要知道何时停止把请求送进死路);第 3 层保护的是端到端可用性(前两层都健康但服务不可用)。

**关键坑(必须遵守)**:yamux 自带 keepalive 必须在两端都关掉(`EnableKeepAlive = false`)。长轮询挂起时 PING 无法在 yamux 的 `ConnectionWriteTimeout`(10s)内完成往返,会产生**假的"连接已死"信号**。这条在 `HttpBroker/internal/broker/relay.go` 有明确注释,不要在实现时"顺手打开"。

---

## 八、服务端详细设计

### 8.1 `internal/registry/` —— 注册表与后端池

```go
// registry.go
type ClientTunnel struct {
    ID          string        // 【F】跨重连稳定,来自 Client 配置的 client_id
    Subdomain   string
    Session     *transport.Session
    Yamux       *yamux.Session
    ConnectedAt time.Time
    RemoteAddr  string

    LocalHealthy  atomic.Bool   // 【D3】
    ActiveStreams atomic.Int64  // 【D6】并发流计数
}

func (t *ClientTunnel) Alive() bool     // yamux 未关闭 && session 未过期
func (t *ClientTunnel) Close() error

type Registry struct {
    mu       sync.RWMutex
    byName   map[string]*TunnelPool     // 【F】子域名 → 后端池
    bySession map[string]*transport.Session  // session_id → Session,供 handlePoll 快速查找
}

func (r *Registry) Register(t *ClientTunnel, maxPerSubdomain int) error  // 409 时返回 ErrPoolFull
func (r *Registry) GetSession(id string) (*transport.Session, bool)
func (r *Registry) Pool(subdomain string) (*TunnelPool, bool)
func (r *Registry) Subdomains() []string                 // 供 autocert HostPolicy
func (r *Registry) SetLocalHealth(sub, clientID string, ok bool)
func (r *Registry) Remove(sessionID string)
func (r *Registry) Sweep(timeout time.Duration) int       // 【A3】5s 一跳,返回驱逐数
func (r *Registry) Snapshot() []TunnelStatus              // 【D9】供 /status
```

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

### 8.2 `internal/router/host.go`

```go
// Subdomain 从 Host 头解析子域名。
//   "myapp.httphop.io:443" + root "httphop.io"  →  "myapp", nil
//   "httphop.io"                                 →  "", ErrNoSubdomain
//   "evil.com"                                   →  "", ErrRootMismatch
//   "a.b.httphop.io"                             →  "", ErrNestedSubdomain(MVP 只支持单层)
func Subdomain(host, rootDomain string) (string, error)

// HostPolicy 返回给 autocert 的回调:只允许控制域名 + 当前已注册的子域名申请证书。
// autocert 不支持通配符/DNS-01,所以必须逐域名签发。
func HostPolicy(reg *registry.Registry, root, controlHost string) autocert.HostPolicy
```

> **附带好处**:逐域名单证书意味着每张证书只覆盖一个名字,浏览器不会跨子域名做连接合并 —— 顺带消除了 C2 那类风险的残余。

### 8.3 `internal/server/server.go` —— 控制面

```go
type Server struct {
    cfg      config.ServerConfig
    registry *registry.Registry
    auth     *TokenStore
    proxy    *httputil.ReverseProxy
    bufPool  sync.Pool          // 【B1】poll_buffer_size 大小的 []byte
    logger   *zap.Logger
    done     chan struct{}
    stopOnce sync.Once
}

func NewServer(cfg, logger) *Server
func (s *Server) Start() error          // 起 :80 和 :443,起 sweepLoop
func (s *Server) Stop(ctx) error        // 优雅停机(顺序见下)

// 控制面 handler
func (s *Server) handleConnect(w, r)    // §6.1
func (s *Server) handlePoll(w, r)       // §6.2
func (s *Server) handleDelete(w, r)     // §6.3
func (s *Server) handleStatus(w, r)     // §6.4

// 【A3】5 秒一跳的清理循环
func (s *Server) sweepLoop()
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

**优雅停机顺序**(照抄 HttpBroker `server.go:141-161` 的做法):

```
1. httpSrv.Shutdown(ctx) —— 停止接受新连接,在途请求排空
2. 关闭所有 Session —— pipe 关闭 → yamux 收到 EOF → 关闭 →
   客户端的 poll 拿到 410(A5)→ 立即进入重连,而不是空转到超时
3. close(s.done) 停掉 sweepLoop
```

### 8.4 `internal/server/proxy.go` —— 公网请求处理

**关键设计**:**在外层 handler 里做查找和检查**(这样能返回准确的 404/503),**只把选中的 tunnel 塞进 context**;`Rewrite` 只负责 header,`DialContext` 只负责开流。

```go
type ctxKey struct{}

func (s *Server) servePublic(w http.ResponseWriter, r *http.Request) {
    sub, err := router.Subdomain(r.Host, s.cfg.RootDomain)
    if err != nil { writeHTTPError(w, 400, "invalid host"); return }

    pool, ok := s.registry.Pool(sub)
    if !ok { writeHTTPError(w, 404, "tunnel not registered"); return }

    tun, ok := pool.Pick(r)
    if !ok { writeHTTPError(w, 503, "no available backend"); return }

    if !tun.LocalHealthy.Load() {
        writeHTTPError(w, 503, "backend local service unhealthy"); return   // 【D3】
    }
    if tun.ActiveStreams.Load() >= int64(s.cfg.MaxStreamsPerTunnel) {
        writeHTTPError(w, 503, "tunnel stream limit reached"); return       // 【D6】
    }

    r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, tun))
    s.proxy.ServeHTTP(w, r)
}

func (s *Server) newProxy() *httputil.ReverseProxy {
    return &httputil.ReverseProxy{
        Rewrite: func(pr *httputil.ProxyRequest) {
            pr.Out.URL.Scheme = "http"
            pr.Out.URL.Host   = "tunnel"        // 占位,DialContext 忽略它
            pr.Out.Host       = pr.In.Host      // 保持原始 Host 传给内网服务
            pr.SetXForwarded()                  // 【D1】注入 XFF/XFP/XFH 并清空伪造值
        },
        Transport: &http.Transport{
            DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
                tun, _ := ctx.Value(ctxKey{}).(*registry.ClientTunnel)
                if tun == nil { return nil, errNoTunnel }
                return bridge.OpenStream(tun)   // §8.5
            },
            DisableKeepAlives:     true,   // 一请求一流;复用由 yamux 承担
            ForceAttemptHTTP2:     false,  // 隧道内固定 HTTP/1.1
            ResponseHeaderTimeout: s.cfg.ResponseHeaderTimeout,  // 【D7】60s,只管响应头
        },
        FlushInterval: -1,                 // 【C】SSE/流式立即刷出,不设会被缓冲
        ErrorHandler:  s.proxyErrorHandler,
        ErrorLog:      zap.NewStdLog(s.logger),
    }
}

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
    Subdomain  string
    Token      string
    MaxClients int    // 【F】默认 1
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

### 9.1 `internal/client/client.go` —— 重连与 Accept 循环

改编自 `HttpBroker/internal/provider/client.go`。

```go
type Client struct {
    cfg     config.ClientConfig
    handler *StreamHandler
    health  *health.Checker
    logger  *zap.Logger
}

// Run 连接服务端并持续接受流;断线后指数退避重连(1s → 3min,翻倍,成功后重置)。
// 阻塞直到 ctx 取消。
func (c *Client) Run(ctx context.Context) error {
    const initialBackoff, maxBackoff = 1 * time.Second, 3 * time.Minute
    backoff := initialBackoff

    go c.health.Run(ctx)          // 【D3】健康检查独立于隧道生命周期常驻

    for {
        if ctx.Err() != nil { return ctx.Err() }

        connector := &transport.HTTPConnector{
            PollInterval:        c.cfg.Transport.PollInterval,   // 【D10】默认 0
            PollTimeout:         c.cfg.Transport.PollTimeout,
            PollGrace:           c.cfg.Transport.PollGrace,      // 【A1】
            SendTimeout:         c.cfg.Transport.SendTimeout,    // 【A1】
            WriteCoalesceWindow: c.cfg.Transport.CoalesceWindow,
            MaxSendChunk:        c.cfg.Transport.MaxSendChunk,   // 【A2】
            AuthToken:           c.cfg.Server.Token,
            ClientID:            c.cfg.ClientID,                 // 【F】
            LocalHealth:         c.health.Healthy,               // 【D3】
            Logger:              c.logger,
        }

        conn, err := connector.Connect(c.cfg.Server.URL)
        if err != nil {
            c.logger.Error("connect failed, will retry", zap.Error(err), zap.Duration("in", backoff))
            if !sleepOrDone(ctx, backoff) { return ctx.Err() }
            backoff = min(backoff*2, maxBackoff)
            continue
        }

        backoff = initialBackoff        // 连上了就重置退避
        c.runSession(ctx, conn)          // 阻塞直到断线或 ctx 取消
        conn.Close()

        if ctx.Err() != nil { return ctx.Err() }
        c.logger.Warn("connection lost, reconnecting", zap.Duration("in", backoff))
        if !sleepOrDone(ctx, backoff) { return ctx.Err() }
        backoff = min(backoff*2, maxBackoff)
    }
}

func (c *Client) runSession(ctx context.Context, conn transport.Conn) {
    sess, err := yamux.Server(conn, transport.YamuxConfig())   // Client 是 yamux.Server
    if err != nil { return }
    defer sess.Close()
    c.acceptStreams(ctx, sess, conn)
}

// acceptStreams 同时监听四个退出条件 —— 缺任何一个都会导致重连不及时。
func (c *Client) acceptStreams(ctx context.Context, sess *yamux.Session, conn transport.Conn) {
    acceptCh := make(chan acceptResult, 1)
    go func() {
        for {
            stream, err := sess.Accept()
            acceptCh <- acceptResult{stream, err}
            if err != nil { return }
        }
    }()

    for {
        select {
        case <-ctx.Done():               return   // 进程退出
        case <-conn.TransportFailed():   return   // 【A1/A5】传输层失败 → 重连
        case <-sess.CloseChan():         return   // yamux 会话关闭
        case res := <-acceptCh:
            if res.err != nil { return }
            go c.handler.Handle(res.stream)
        }
    }
}
```

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

结果通过 `HTTPConnector.LocalHealth` 传给 `HTTPConn`,由 `pollLoop` 搭车在 `X-Local-Health` 头上报 —— **不需要额外的控制流或控制协议**,因为 poll 请求本来就每 `poll_timeout` 至少来一次。

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
    PollTimeout    time.Duration `mapstructure:"poll_timeout"`     // 30s
    SessionTimeout time.Duration `mapstructure:"session_timeout"`  // 【A3】60s
    SweepInterval  time.Duration `mapstructure:"sweep_interval"`   // 【A3】5s
    CoalesceWindow time.Duration `mapstructure:"coalesce_window"`  // 2ms
    PollBufferSize int           `mapstructure:"poll_buffer_size"` // 【B1】262144
    MaxRequestBody int           `mapstructure:"max_request_body"` // 【A2】1048576
    PollMode       string        `mapstructure:"poll_mode"`        // 【B1】"batch";"stream" 未实现
    MaxStreamsPerTunnel int      `mapstructure:"max_streams_per_tunnel"` // 【A4/D6】256
}

type ProxyConfig struct {
    ResponseHeaderTimeout time.Duration `mapstructure:"response_header_timeout"` // 【D7】60s
    ReadHeaderTimeout     time.Duration `mapstructure:"read_header_timeout"`     // 【D6】10s
    MaxHeaderBytes        int           `mapstructure:"max_header_bytes"`        // 【D6】65536
}

type TunnelBinding struct {
    Subdomain  string `mapstructure:"subdomain"`
    Token      string `mapstructure:"token"`
    MaxClients int    `mapstructure:"max_clients"`   // 【F】默认 1
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
    PollTimeout    time.Duration `mapstructure:"poll_timeout"`     // 30s(可由 connect 响应覆盖)
    PollGrace      time.Duration `mapstructure:"poll_grace"`       // 【A1】10s
    SendTimeout    time.Duration `mapstructure:"send_timeout"`     // 【A1】15s
    DialTimeout    time.Duration `mapstructure:"dial_timeout"`     // 10s
    CoalesceWindow time.Duration `mapstructure:"coalesce_window"`  // 2ms
    MaxSendChunk   int           `mapstructure:"max_send_chunk"`   // 【A2】524288
}

type HealthConfig struct {
    Enabled  bool          `mapstructure:"enabled"`   // 【D3】true
    Mode     string        `mapstructure:"mode"`      // "tcp" | "http"
    Path     string        `mapstructure:"path"`      // "/healthz"
    Interval time.Duration `mapstructure:"interval"`  // 15s
    Timeout  time.Duration `mapstructure:"timeout"`   // 3s
}
```

**配置校验**(启动时执行,失败直接退出并给出可操作的提示):

- `client_id` 非空;`root_domain` / `control_host` 非空且 `control_host` 以 `root_domain` 结尾。
- `session_timeout ≥ poll_timeout × 2`,否则健康 Client 会被误判掉线 —— **这是最容易配错的一项,必须校验并给出明确报错**。
- `max_request_body ≥ max_send_chunk × 2`(A2)。
- `poll_mode == "batch"`,否则报"stream 模式尚未实现"。
- `tunnels` 里 subdomain 不重复、token 不重复且长度 ≥ 32。

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
  max_request_body: 1048576   # 1MB,必须 ≥ 客户端 max_send_chunk × 2
  poll_mode: "batch"          # "stream" 尚未实现
  max_streams_per_tunnel: 256

proxy:
  response_header_timeout: 60s  # 只约束响应头阶段,不影响 SSE/WebSocket/大文件
  read_header_timeout:     10s
  max_header_bytes:        65536

tunnels:
  - subdomain: "myapp"
    token: "REPLACE_WITH_32B_RANDOM_TOKEN_AAAA"
    max_clients: 1            # >1 即开启多后端(LB 策略尚未实现)
  - subdomain: "api"
    token: "REPLACE_WITH_32B_RANDOM_TOKEN_BBBB"

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
  poll_interval:   0s         # 0 = 收到响应立即重发,等待交给服务端的长轮询
  poll_timeout:    30s
  poll_grace:      10s        # ResponseHeaderTimeout = poll_timeout + poll_grace
  send_timeout:    15s
  dial_timeout:    10s
  coalesce_window: 2ms
  max_send_chunk:  524288     # 512KB

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
| 404 | 子域名未在配置中绑定,或从未有 Client 注册 | `servePublic` |
| 502 | 隧道通,但 Client 拨号本地服务失败,或响应读取出错 | `proxyErrorHandler` |
| 503 | 池内无可用后端 / 本地服务不健康(D3)/ 并发流达上限(D6) | `servePublic` |
| 504 | 超过 `response_header_timeout` 仍未收到响应头 | `proxyErrorHandler` |

### 控制面(返回给 Client)

| 状态码 | 触发条件 | Client 的反应 |
|---|---|---|
| 200 | 成功 | 继续 |
| 204 | 长轮询超时无数据 | **正常**,立即重新 poll |
| 400 | 缺 `client_id` / 请求体读取失败 | 记日志,重连 |
| 401 | token 无效 | **致命** → 传输失败 → 退避重连(配置多半错了,退避会拉到 3min) |
| 404 | 会话不存在 | **致命** → 传输失败 → 重连 |
| 409 | 该子域名已达 `max_clients` | 记日志,退避重连 |
| **410** | 【A5】服务端主动关闭了会话 | **致命** → 传输失败 → **立即重连** |
| **413** | 【A2】请求体超上限 | **可恢复** → `max_send_chunk` 减半后重试,**不断开隧道** |

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
│   ├── transport/                  # ★ 地基,从 HttpBroker 移植
│   │   ├── pipe.go                 #   BufferedPipe + ReadAvailable(+A4 高水位)
│   │   ├── pipe_test.go            #   移植自 HttpBroker
│   │   ├── session.go              #   Session(改名 + A3 pollInFlight)
│   │   ├── httpconn.go             #   HTTPConn(+A1 +A2 +A5 +D3 +D10)
│   │   ├── httpconn_test.go        #   移植自 HttpBroker
│   │   └── transport.go            #   Conn 接口、HTTPConnector、YamuxConfig()
│   ├── registry/
│   │   ├── registry.go             #   Registry、ClientTunnel、Sweep
│   │   └── pool.go                 #   ★F TunnelPool + Balancer + firstAvailable
│   ├── router/
│   │   └── host.go                 #   Subdomain 解析 + autocert HostPolicy
│   ├── server/
│   │   ├── server.go               #   控制面 connect/poll/delete/status + sweepLoop
│   │   ├── proxy.go                #   ★C ReverseProxy + servePublic + DialContext
│   │   ├── bridge.go               #   ★C OpenStream / Bridge(为裸 TCP 预留)
│   │   ├── auth.go                 #   TokenStore + AuthMiddleware
│   │   ├── tls.go                  #   autocert + :80 挑战与跳转 + 证书预热
│   │   └── errors.go               #   writeHTTPError / writeJSONError
│   ├── client/
│   │   ├── client.go               #   重连退避 + yamux Accept 循环
│   │   ├── handler.go              #   拨号本地服务 + 双向桥接 + host 改写
│   │   └── health.go               #   ★D3 本地服务健康检查
│   └── config/
│       ├── config.go               #   viper 加载 + 默认值
│       └── validate.go             #   启动时校验(尤其 session_timeout ≥ 2×poll_timeout)
├── configs/
│   ├── server.example.yaml
│   └── client.example.yaml
└── test/
    └── integration_test.go         # 进程内端到端
```

**★ 标记的是最关键的文件**:`internal/transport/*`(地基)、`internal/server/proxy.go`(HttpHop 特有,HttpBroker 无对应物)、`internal/registry/pool.go`(LB 结构预留)。

---

## 十四、依赖

| 用途 | 选择 | 说明 |
|---|---|---|
| 长轮询传输 | 移植 `~/source/HttpBroker/internal/transport` | 已解决空闲开销/队头阻塞/连接数问题,不重新设计 |
| 多路复用 | `github.com/hashicorp/yamux` | 与 HttpBroker 一致;`EnableKeepAlive=false`、`MaxStreamWindowSize=64KB` |
| 公网反向代理 | 标准库 `net/http/httputil` | §3.C |
| 控制面路由 | `github.com/gorilla/mux` | 匹配 `/tunnel/{id}/poll` 风格 |
| 配置 | `github.com/spf13/viper` + `mitchellh/mapstructure` | 与 HttpBroker 一致 |
| 日志 | `go.uber.org/zap` | 结构化日志 |
| 自动 TLS | `golang.org/x/crypto/acme/autocert` | HTTP-01 逐域名签发(不支持通配符) |
| 测试 | 标准库 `testing` + `net/http/httptest` | HttpBroker 的 `pipe_test.go`/`httpconn_test.go` 可直接参考 |

---

## 十五、分阶段实现顺序与验收

每一阶段都要能独立验证再进入下一阶段。**不要跳过阶段 1 的测试** —— 后面所有东西都建在它上面。

### 阶段 0:项目骨架与仓库初始化

1. `git init`,添加上游 `https://github.com/DiamondGo/HttpHop.git`(**推送前先 `git ls-remote` 确认远端是否已有内容**,避免覆盖别人的提交)。
2. `go mod init github.com/DiamondGo/HttpHop`(Go ≥ 1.21)。
3. 建目录骨架;`.gitignore`(Go 模板 + 二进制产物 + `configs/*.local.yaml`);`Makefile`(build / test / lint)。
4. `README.md`:项目定位 + 快速上手占位 + **§18 已知限制**(尤其上下行吞吐不对称、前置 CDN 会截断长轮询)。
5. 把本设计文档复制进仓库的 `plans/architecture.md`(与 HttpBroker 的目录习惯一致),让设计与代码同仓演进。
6. 首次提交并 `git push -u origin <默认分支>`。

**验收**:`go build ./...` 通过(空实现);`git log` 有首次提交;远端可见。

### 阶段 1:传输层 ★ 最关键

`internal/transport/{pipe.go,session.go,httpconn.go,transport.go}` + 移植的单测。落地 **A1、A2、A4、A5(客户端侧)、D3(上报通道)、D10**。
**验收**:

- `pipe_test.go` 全绿,含 `ReadAvailable` 两阶段行为(超时返回 0、合并窗口攒够再返回、关闭返回 EOF)。
- 新增测试:`flushLoop` 分片正确、遇 413 减半重试且不断开、`maxSendChunk` 边界。
- 新增测试:用 `httptest` 起一个"永不响应"的假服务端,断言 `pollLoop` 在 `poll_timeout + grace` 内触发 `TransportFailed()`(**这是 A1 的直接验收**)。

### 阶段 2:配置

`internal/config/{config.go,validate.go}` + 两个 example yaml。
**验收**:能加载示例配置;`session_timeout < 2×poll_timeout` 时报出明确错误。

### 阶段 3:注册表与路由

`internal/registry/{registry.go,pool.go}`、`internal/router/host.go`。
**验收**:单测覆盖 —— 子域名解析的各种边界;同 `client_id` 重连是替换而非新增;`max_clients` 达上限返回 `ErrPoolFull`;`Sweep` 正确驱逐过期会话。

### 阶段 4:服务端控制面

`internal/server/{server.go,auth.go,errors.go}`。落地 **A3、A5(服务端侧)、B1 第一层、D3(接收侧)**,含 `handleConnect` 的预注册竞态修复。
**验收**:用 `httptest` 走完 connect → poll → delete 全流程;断言 EOF 时返回 410 而非 204;断言超限返回 413;断言 `pollInFlight` 计数正确。

### 阶段 5:服务端公网侧 ★ HttpHop 特有

`internal/server/{proxy.go,bridge.go}`。
**验收**:`httptest` 起一个假 yamux 对端,断言请求被正确转发、`X-Forwarded-For` 被注入、伪造的入站 XFF 被剥离、错误路径返回正确状态码。

### 阶段 6:客户端

`internal/client/{client.go,handler.go,health.go}`。
**验收**:能连上阶段 4 的服务端并保持;杀掉服务端后按退避重连;健康检查状态变化能反映到 `/status`。

### 阶段 7:main 与 TLS

`cmd/server/main.go`、`cmd/client/main.go`、`internal/server/tls.go`。
**验收**:两个二进制能用示例配置起来;本地自签证书场景下完整跑通一次请求。

### 阶段 8:集成测试与文档

`test/integration_test.go`;README(**必须写明上下行吞吐不对称**);部署说明(systemd unit 样例)。
**验收**:§16 的全部用例通过。

---

## 十六、测试计划

### 单元测试

| 目标 | 关键用例 |
|---|---|
| `BufferedPipe` | 阶段一超时返回 `(0, nil)`;阶段二合并窗口;关闭后返回 EOF;并发读写无 race(`-race`) |
| `HTTPConn` | 分片发送;413 减半重试;**永不响应的服务端在 `poll_timeout+grace` 内触发 `TransportFailed`**;404/410/401 立即失败 |
| `Session` | `pollInFlight` 增减;`IsExpired` 边界;`Close` 幂等 |
| `Registry`/`Pool` | 同 ID 替换;`max_clients` 上限;`Sweep` 驱逐;并发注册无 race |
| `router.Subdomain` | 带端口、根域、多层子域、域名不匹配、大小写 |
| `config.Validate` | 各项约束尤其 `session_timeout ≥ 2×poll_timeout` |

### 集成测试(`test/integration_test.go`,进程内)

起 server + client + 本地 echo 服务,全部在同一进程内用随机端口:

1. **基本转发**:请求经隧道到达 echo 服务,响应正确。
2. **多租户路由**:两个 client / 两个 token / 两个子域名,按 Host 正确分流。
3. **并发**:50 个并发请求不串行化(总耗时应接近单个请求而非 50 倍)—— 验证 yamux 多路复用 + 读写分离确实消除了队头阻塞。
4. **重连**:关掉 server 再起来,client 自动重连,期间的请求返回 503/504 而非挂死。
5. **410 快速重连**(A5):server 主动关闭会话,断言 client 在**秒级**内重连,而不是等到 `session_timeout`。
6. **413 不断链**(A2):构造超过 `max_send_chunk` 的响应体,断言隧道**不被重置**且请求成功。
7. **X-Forwarded-For**(D1):echo 服务回显请求头,断言拿到真实调用方 IP;再从公网侧伪造 `X-Forwarded-For: 1.2.3.4`,断言它被**剥离**而非被信任。
8. **本地服务健康**(D3):停掉 echo 服务,断言 `/status` 在 `interval` 内把 `local_health` 置为 `down`,且该子域名开始返回 503。
9. **LB 结构未退化**(F):`max_clients: 1` 时第二个 client 注册返回 409;同 `client_id` 重连是替换;重连后 `ClientTunnel.ID` 不变。

### 手工/端到端验证

10. **心跳三层验收(需求 #3 的核心)**:
    - a. `kill -9` 客户端 → 服务端应在 60s 内驱逐;若有 TCP FIN,`poll_in_flight` 应**立即**归零。
    - b. `iptables -A OUTPUT -d <server> -j DROP` 模拟静默黑洞 → **客户端**应在 ~40s 内检测到并进入重连(**A1 的直接验收**)。
    - c. 只停本地服务、保持隧道 → `/status` 应在 15s 内标记 `local_health: down`(**D3 的直接验收**)。
11. **吞吐**:`tc netem` 注入 100ms RTT,下载 100MB 文件;对比 `poll_buffer_size` 64KB vs 256KB,预期 ×4(**B1 第一层的量化验收**,同时作为将来做流式 poll 的性能基线)。
12. **WebSocket**:通过隧道建立 WS 连接并双向收发若干消息。
13. **SSE**:通过隧道消费一个 SSE 流,断言事件**逐条实时到达**而非被缓冲(验证 `FlushInterval: -1`)。
14. **HTTP/2**:`curl --http2` 正常工作(这是 §3.C 相对裸桥接方案的直接收益)。
15. **大请求体**:POST 5MB body 成功且隧道不重置。
16. **证书签发**:真实域名下走一次 autocert HTTP-01,确认 `HostPolicy` 只放行已注册子域名。

---

## 十七、MVP 范围与推后项

MVP = **第一个能真正部署、能把内网服务可靠暴露到公网的版本**,不是功能齐全的版本。划线依据是"缺了它交付物就是坏的",而非功能大小。

### MVP 必须包含

**因为它们是缺陷不是功能**(推后等于交付一个已知会断的东西):A1、A2、A3、A4、A5。

**因为它们是产品定义的一部分**:

- 传输层移植 + 单测(阶段 1)
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
| **B1 第二层:流式 chunked poll** | 要重写 `pollLoop` / `handlePoll` 两个地基函数,应等 MVP 跑通、有测试覆盖后再动;且需实测前置 CDN 对 chunked 的缓冲行为。`poll_mode` 配置项现在就预留 |
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
2. **单层子域名**:`a.b.httphop.io` 不支持。autocert 用 HTTP-01,无法签发通配符证书。
3. **响应已开始后无法降级**:响应头已发出再断连,只能中断连接,无法改成 502 —— 这是 HTTP 的固有限制,不是本设计的缺陷。
4. **前置 CDN/反向代理需要额外配置**:长轮询会被某些默认配置(如 nginx 的 `proxy_read_timeout 60s`)截断;`poll_timeout` 需相应调小。
5. **`session_timeout` 与 `poll_timeout` 强耦合**:前者必须 ≥ 后者的 2 倍,配置校验会强制这一点。
6. **本地健康检查只覆盖"能否连通/返回 2xx"**,不代表业务层健康。
7. **单 Client 只能暴露一个本地目标**:多目标需要多开 Client 实例(或等后续版本)。
