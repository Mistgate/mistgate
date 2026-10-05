---
title: Telegram alerts
description: Send the panel's alerts to Telegram. Set up the bot with BotFather, link your chat, what is sent and to whom, and how the mirror Worker can use the same bot.
---

The panel can write to your Telegram when something needs a human: a node goes down or comes back, a check fails, a backup fails, a new release is out, an agent's plan waits for your approval. It is **off until the owner sets a bot up**, and it only sends alerts: there are no commands, nothing to approve or change from the chat. An approval stays in the admin, behind a passkey or an authenticator code.

## Set up the bot (owner)

1. In Telegram, open [@BotFather](https://t.me/BotFather), send `/newbot` and follow the questions. Give the bot a name and a username of your own; do not reuse a bot that another program reads.
2. Copy the token BotFather sends (`123456789:AA…`).
3. In the admin, open **Integrations → Telegram alerts**, paste the token under **Bot** and choose **Save bot**. The panel asks for a fresh sign-in confirmation, checks the token with Telegram (`getMe`) and shows the bot's `@username`.

The token is encrypted with the panel master key, like the other secrets, and is never returned by the API: the screen shows "Token is set" and the username only. **Replace token** sets another one; **Remove bot** forgets it. A different bot, or none, **unlinks every chat** (a chat belongs to a bot); the same bot pasted again keeps them. Saving and removing the bot are written to the [audit log](security.md) (`telegram_bot_set`, `telegram_bot_clear`) with the bot's username and never the token.

The panel never takes a webhook: it asks Telegram for new messages (long polling) only to hear the linking code below, so it works behind the decoy site with no inbound port. Telegram does not let a bot with a webhook be polled, so setting the bot **removes a webhook** left on it. Give the panel a bot that nothing else reads: if another program polls it, the card says so ("Another program reads this bot's messages").

## Link your chat (every admin)

Each admin links their own chat; the owner does not link anyone else. Open **Integrations → Telegram alerts → Link my Telegram** (a helper or a read-only admin sees this part of the card too, once the owner has set a bot):

1. The panel asks for a fresh sign-in confirmation and shows a one-time code and a button **Open in Telegram** (`https://t.me/<bot>?start=<code>`).
2. In Telegram, press **Start**. If you prefer, send `/start <code>` to the bot yourself.
3. The panel hears it, binds that chat to your admin account and the bot answers "Linked". The card shows **Linked** within a few seconds.

The code works **once** and for **10 minutes**; a new code replaces the old one. Only a private chat can be linked, not a group. A chat sending anything else to the bot gets no reply at all, so a stranger who finds the bot learns nothing from it. A chat belongs to one admin: linking it to another admin moves it.

Once linked you can switch **Send me alerts** off without unlinking, **Send test** (one message to your chat, at most one every 10 seconds), or **Unlink**. Linking and unlinking are written to the audit log (`telegram_link`, `telegram_unlink`). The card also shows your **Chat ID**, which the mirror Worker needs (below).

## What is sent, and to whom

| What | Who gets it | When |
|:--|:--|:--|
| A [health alert](health.md): node unreachable, alive but no traffic, a failing check, the node doctor's failures (a full disk, WARP down, the AmneziaWG module and so on), a certificate about to expire, a paused update | Every linked admin, whatever the role (every role can read them in the admin) | When it opens, and again when it resolves, with how long it lasted |
| An AmneziaWG kernel module that failed to build on a node | Owner | Once per failure |
| A new signed panel release | Owner | Once per version |
| Node agents behind the release | Owner | Once per version, with the names of the nodes |
| A failed [backup](backups.md) | Owner | Once, however often the schedule retries; and when the next backup works |
| An MCP plan waiting for your approval | Owner | When the plan is made, with a link to **Integrations → Waiting for you**. You decide in the admin, never in the chat |
| A lockout after too many failed sign-ins | Owner | When it happens |
| A sign-in from an address this panel has not seen lately; a passkey added or removed, a password or authenticator changed | Owner, and the admin it is about | When it happens |

How the messages behave:

- **Bursts become one message.** Events that arrive within about 30 seconds of each other are sent together, one message per chat; an identical line is not repeated.
- **The same open alert is never sent twice.** When it resolves you get one message that says how long it lasted. An alert that a worse one replaced, one you accepted as normal and one you muted end without a message.
- **Info-level alerts are not sent.** Only warnings and critical ones are (a fixed floor in this version; there are no quiet hours or filters yet).
- **Short, in the panel language.** Messages are in Russian or English, whichever the panel's default language is (**Settings → Interface → Default language**). Names that come from data, such as a node, a profile or a token, are escaped and cut to one line.
- **Never a secret.** A message holds no token, no subscription link and no password.
- **Links only when the panel knows its public address.** With one, a message ends with a link to the right admin page (Health, Updates, Integrations, the backup settings). Without it (the admin is on a separate local listener), the card says so and messages carry no links. Mind that the link holds the address of your hidden admin: it is in the chat history on Telegram's servers, so link only a private chat on an account you trust.

## Delivery

The panel connects out to `api.telegram.org` over HTTPS; the host must be able to reach it. A failed send is retried with a growing wait; a "too many requests" answer is waited out for as long as Telegram asks; a chat that cannot be written to (you blocked the bot) is not retried. The messages waiting in memory when the panel stops are sent first, within a few seconds; a crash loses them. If Telegram refuses the token, or the panel cannot reach it, the card shows it under **Bot**.

## The mirror Worker can use the same bot

When the panel itself is down, the panel cannot tell you. The Cloudflare mirror Worker is a separate program that can: it can send its "the panel is down" and "back up" messages through the same bot and to the same chat. It needs the **bot token** (from BotFather: `/mybots` → your bot → **API Token**; the panel never shows it again) and your **Chat ID** (shown on your linked card). The Worker only sends (`sendMessage`); it must not set a webhook or poll the bot, or the panel can no longer hear the linking code.

## Who can do what

| | Owner | Helper | Read only | API token / MCP |
|:--|:--|:--|:--|:--|
| Set, replace or remove the bot | yes (fresh sign-in confirmation) | no | no | never |
| Link, unlink, switch alerts, send a test for their own chat | yes (linking needs a fresh sign-in confirmation) | yes | yes | never |
| See who linked a chat | yes | no | no | never |

Nothing in this feature is open to API tokens or MCP.
