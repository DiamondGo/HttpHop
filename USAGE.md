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
