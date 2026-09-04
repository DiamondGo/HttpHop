  日常使用

  在项目根目录执行：

  ┌──────────────────────────────┬────────────────────────┐
  │ 操作                         │ 命令                   │
  ├──────────────────────────────┼────────────────────────┤
  │ 安装并启动（登录后自动运行） │ make service-install   │
  ├──────────────────────────────┼────────────────────────┤
  │ 启动                         │ make service-start     │
  ├──────────────────────────────┼────────────────────────┤
  │ 停止                         │ make service-stop      │
  ├──────────────────────────────┼────────────────────────┤
  │ 重启                         │ make service-restart   │
  ├──────────────────────────────┼────────────────────────┤
  │ 查看状态                     │ make service-status    │
  ├──────────────────────────────┼────────────────────────┤
  │ 看日志                       │ make service-logs      │
  ├──────────────────────────────┼────────────────────────┤
  │ 完全卸载                     │ make service-uninstall │
  └──────────────────────────────┴────────────────────────┘

  也可以直接用脚本：

  ./scripts/macos/client-service.sh start|stop|status|logs

  行为说明

  • 登录后自动启动：LaunchAgent 已写入 ~/Library/LaunchAgents/com.httphop.client.plist，RunAtLoad + KeepAlive 已开启。
  • 停止后保持关闭：stop 会 unload 并 disable，直到你手动 start 或 install 才会再跑（不会在下次登录时偷偷起来，除非你再次 install）。
  • 日志位置：local/logs/httphop-client.log 和 local/logs/httphop-client.err.log
  • 配置：默认使用 local/client.yaml；可用环境变量覆盖：
    • HTTPHOP_CLIENT_CONFIG
    • HTTPHOP_CLIENT_BIN

  如果改了 local/client.yaml，执行 make service-restart 即可生效。

  多服务配置

  一个 client 进程可以同时隧道多个本地服务到不同的远程 server。
  在 client.yaml 中使用 services 列表，每个 service 指定自己的 server、token 和本地目标：

    transport:
      poll_interval: 0s
      poll_grace: 10s
      prefer_websocket: true
      prefer_resume: true
      max_replay_bytes: 16777216

    health:
      enabled: true
      mode: "tcp"

    services:
      - client_id: "llm"
        token_file: "secrets/llm.token"
        local:
          target: "127.0.0.1:54000"
        server:
          url: "https://llm.example.com"
          control_path: "/tunnel"

      - client_id: "blog"
        token_file: "secrets/blog.token"
        local:
          target: "127.0.0.1:3000"
        server:
          url: "https://blog.example.com"
          control_path: "/tunnel"

  • transport、health、logging 为共享默认值，各 service 可通过同名字段覆盖 health。
  • server（url/control_path/insecure_skip_verify）和 token_file 在每个 service 中单独配置。
  • 旧的单服务格式（顶层 client_id + local + server）仍然兼容。

  Resume 配置升级

  pollmux v0.2 可在 WebSocket（或上下行均为 stream）短暂断线后恢复同一个
  yamux 会话，使正在处理的 HTTP 请求继续执行。建议在 server.yaml 中加入：

    tunnel:
      enable_websocket: true
      enable_resume: true
      resume_grace: 30s
      max_replay_bytes: 16777216
      max_detached_resumable: 1024

  client.yaml 的 transport 中加入：

    prefer_websocket: true
    prefer_resume: true
    max_replay_bytes: 16777216

  新版本默认启用 enable_resume/prefer_resume，但只有同时启用 WebSocket 或双向
  stream 时才能协商成功；batch 模式仍可正常工作，但不能保持断线时的活动请求。
  修改后先重启 server，再重启 client。旧客户端与新服务端可以混用。

  内存受 max_replay_bytes（每条隧道、每个方向）和服务端
  max_detached_resumable 控制；负值表示不限制 detached 会话数，小内存服务器应
  使用较小的正值。反向代理的请求超时应大于 resume_grace，并确保
  /tunnel/{id}/resume 与其他 /tunnel 路径走相同代理
  和鉴权规则。
