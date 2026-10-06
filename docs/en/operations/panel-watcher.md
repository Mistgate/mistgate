---
title: Panel watcher
description: Use a Cloudflare Worker to check the panel once a minute and send Telegram alerts when it stops answering and recovers.
---

The panel watcher is a small Cloudflare Worker that probes the panel's public listener once a minute. After three consecutive failures it sends one Telegram message; when the panel answers again, it sends one recovery message with the downtime.

If either Telegram secret is missing, the Worker does nothing.

Any HTTP status below 500 counts as healthy, including the panel's decoy 404 page. A timeout, connection or TLS error, or any 5xx response counts as a failure. The Worker does not proxy traffic, change DNS, or add a route on the panel's domain. Its own public `fetch` handler returns 404, and `workers_dev` is disabled.

## Usage and cost

The schedule runs once a minute: 1,440 checks a day, each with one panel request and one KV read. KV is written only when the failure count changes or the down/up state changes; healthy checks and continued downtime after the alert threshold do not write KV.

## Set it up

From `deploy/cloudflare-watcher`:

1. Copy the example and create a KV namespace:

   ```sh
   cp wrangler.toml.example wrangler.toml
   wrangler kv namespace create WATCH
   ```

2. Put the namespace ID returned by Wrangler into `wrangler.toml` under `[[kv_namespaces]]`.
3. Set `HEALTH_URL` to the panel's public listener, for example `https://panel.example.com/`. The panel's decoy page is enough; do not point the Worker at a private listener.
4. Optionally set `HEALTH_TIMEOUT_MS` (default `8000`), `ALERT_AFTER` (default `3` consecutive failures), `ALERT_NAME` (default `Mistgate panel`) and `ALERT_LANG` (`en` or `ru`, default `en`) in `[vars]`.
5. Add the Telegram bot token as a secret:

   ```sh
   wrangler secret put TELEGRAM_BOT_TOKEN
   ```

   Use the same bot as the panel's Telegram alerts. Get its token from BotFather; the panel does not show it again.
6. Add the chat ID from **Integrations → Telegram alerts** on your own linked card:

   ```sh
   wrangler secret put TELEGRAM_CHAT_ID
   ```

7. Install the package dependencies and deploy:

   ```sh
   pnpm install
   wrangler deploy
   ```

The Worker only calls Telegram's `sendMessage` method. It never polls the bot or configures a webhook, so the panel can continue polling for chat-linking messages.

## Test the alert

Temporarily point `HEALTH_URL` at a closed port on the panel's public host for three minutes, or stop the panel. After three scheduled failures, the Worker should send a down alert. Restore the URL or start the panel again; the next healthy check should send the recovery message. Restore your original `HEALTH_URL` after the test.
