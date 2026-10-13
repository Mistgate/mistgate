---
title: 面板监视器
description: 使用 Cloudflare Worker 每分钟检查一次面板，并在面板停止响应和恢复时发送 Telegram 告警。
---

面板监视器是一个小型 Cloudflare Worker，每分钟探测一次面板的公共监听器。连续失败 3 次后，它会发送一条 Telegram 消息；面板恢复响应后，它会发送一条恢复消息并说明停机时长。

如果缺少任一 Telegram 密钥，Worker 不会执行任何操作。

任何低于 500 的 HTTP 状态都视为正常，包括面板的伪装 404 页面。超时、连接错误、TLS 错误或任何 5xx 响应都视为失败。Worker 不会代理流量、修改 DNS 或在面板的域名上添加路由。它自己的公共 `fetch` 处理程序返回 404，并且已禁用 `workers_dev`。

## 用量和成本

定时任务每分钟运行一次：每天检查 1,440 次，每次向面板发送一个请求并读取一次 KV。只有失败次数或停机 / 恢复状态发生变化时才会写入 KV；检查正常或告警阈值触发后持续停机时都不会写入 KV。

## 设置

在 `deploy/cloudflare-watcher` 中：

1. 复制示例配置并创建 KV 命名空间：

   ```sh
   cp wrangler.toml.example wrangler.toml
   wrangler kv namespace create WATCH
   ```

2. 将 Wrangler 返回的命名空间 ID 填入 `wrangler.toml` 的 `[[kv_namespaces]]`。
3. 将 `HEALTH_URL` 设为面板的公共监听器，例如 `https://panel.example.com/`。面板的伪装页面即可；不要将 Worker 指向私有监听器。
4. 可以在 `[vars]` 中设置 `HEALTH_TIMEOUT_MS`（默认值为 `8000`）、`ALERT_AFTER`（连续失败次数，默认值为 `3`）、`ALERT_NAME`（默认值为 `Mistgate panel`）和 `ALERT_LANG`（`en` 或 `ru`，默认值为 `en`）。
5. 添加 Telegram 机器人令牌作为密钥：

   ```sh
   wrangler secret put TELEGRAM_BOT_TOKEN
   ```

   使用与面板 Telegram 告警相同的机器人。向 BotFather 获取其令牌；面板不会再次显示该令牌。
6. 从自己的已关联卡片上的 **集成 → Telegram 告警** 获取聊天 ID：

   ```sh
   wrangler secret put TELEGRAM_CHAT_ID
   ```

7. 安装软件包依赖并部署：

   ```sh
   pnpm install
   wrangler deploy
   ```

Worker 只会调用 Telegram 的 `sendMessage` 方法。它不会轮询机器人或配置 webhook，因此面板可以继续轮询聊天关联消息。

## 测试告警

临时将 `HEALTH_URL` 指向面板公共主机上的一个关闭端口并保持 3 分钟，或停止面板。连续 3 次定时检查失败后，Worker 应发送停机告警。恢复该 URL 或重新启动面板；下一次正常检查应发送恢复消息。测试后恢复原来的 `HEALTH_URL`。
