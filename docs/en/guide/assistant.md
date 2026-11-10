---
title: Assistant
description: A cloud model in the admin that answers about your nodes, people and errors and prepares changes you apply. Providers, where the data goes, memory, limits and safety.
---

# Assistant

The assistant is a chat in the admin with a cloud model you connect with your own API key. Ask it which nodes had problems, why someone cannot connect, who is over the device limit; ask it to make a group, change the subscription texts or turn a profile off for a node. It looks things up with the panel's own tools and prepares every change as a card in the panel's words. **Nothing changes until you press Apply.**

It works with the rights of the admin who talks to it, never more, and it is available on the VPS edition (see [In the Cloudflare edition](#in-the-cloudflare-edition)).

## Turn it on

The owner sets it up in **AI → Assistant**:

1. Under **Providers**, choose **Connect** on a provider, paste its API key and choose **Check and save**. The panel asks for your passkey or code, checks the key with the provider and seals it; the card then shows only its last four characters.
2. On the connected card, pick the **Model** (the price per million tokens is under it) and the **Monthly budget, $**.
3. Above, turn **The assistant is on**, pick the **Provider the chat uses**, **How hard it thinks** (**Quick**, **Normal**, **Thorough**), **Who may use it** (**Only me**, **Owner and helpers**, **Everyone in the panel**), the **Notes for the assistant** and how long to **Keep conversations**, and choose **Save**.

A round button appears at the bottom corner of every page for the admins it is open to; **Ctrl+J** (**⌘J** on a Mac) opens and closes the chat too.

## Providers and where your data goes

| Provider | Address the panel calls | Models | Where the data goes |
|:--|:--|:--|:--|
| Anthropic | `api.anthropic.com` | from the provider's list; Claude Opus 5.5 by default, Sonnet 5.5 and Haiku 5.5 cheaper | Processed in the United States; API data is not used for training. |
| DeepSeek | `api.deepseek.com` | from the provider's list | Stored in China; the provider's policy allows using what you send for training. |
| Z.ai | `api.z.ai` (the international platform) | GLM-5.3 and GLM-5.3-Flash | Processed in Singapore; the provider's agreement says content is not stored. |
| Ollama Cloud | `ollama.com` | the cloud models that take tools | Processed in the United States; not used for training. |

The card of each built-in provider says the same, so you choose knowing it. The owner can also connect up to five OpenAI-compatible providers: enter a name, API base URL (such as `https://example.com/v1`), model ID, key and optional input and output prices. The panel checks that the model can call a tool before saving. A model can also be chosen from the provider's model list when available. Set its monthly budget on the card, and select it as the main or fallback provider.

The panel calls providers from its own server; keys are sealed and never returned to the browser. Custom endpoints require HTTPS. To use an HTTP endpoint on the panel's local network, explicitly enable local access; the key may be empty for a local model. Link-local addresses are blocked, including cloud metadata endpoints; carrier-NAT addresses (100.64.0.0/10) count as local. Follow the provider's own data policy when choosing where to send the conversation. Prices you enter are estimates: leave them at zero only for a free provider.

What the provider receives: the conversation, the assistant's instructions, your notes and the owner's notes, and the results of the tools it calls. Those results pass the same filter as [MCP](../reference/mcp.md#what-an-agent-never-gets): no subscription links or page passwords, no keys, device configurations or tokens, no server passwords, no node addresses. If a message you type looks like a key or a password, the chat asks before sending it.

## What it can do

- **Read**: nodes and their health, people, groups, devices, events and alerts, reachability from the country, subscriptions and DNS, updates; the audit log for the owner. These are the [MCP](../reference/mcp.md) read tools of your role.
- **Change**: every MCP change, as a plan. The plan comes to the chat as a **Proposed change** card with the panel's own summary, the facts (anything that came from data is quoted) and the danger notes. **Apply** runs exactly that plan, once; **Dismiss** drops it; a plan not applied within 10 minutes expires.
  - A plan that needs the owner (it changes what runs on the nodes, touches many people, or every person would see it): when the owner talks to the assistant, **Approve and apply** asks for the passkey or code and does both; for anyone else the card says it waits for the owner in **AI → Waiting for you**.
  - Installing a node over SSH is approved only in **AI → Waiting for you**, where the owner compares the host key and chooses how the panel logs in.
- **Its rights are yours**: the owner's assistant has the Admin tools, a helper's the Operator tools, a read-only admin's only the read tools.

The model never applies anything itself: it has no tool to do so. Every call is in the audit log as "Assistant of <admin>", with the source **Assistant** (filter by it in **Settings → Audit**).

## Work out what does not open

Ask "who cannot connect, and what do we fix?", or press **Work out what does not open** on **Health → People**: the chat opens with the question ready to send. The assistant starts with `access_report`, which sets protocol against node (and group) over the last day: who tried, who got through, the rejects, stalls, freezes and loss the counters can tell apart, and who was given a server but never connected. It adds the view from the country (operators that see a node dark, which TSPU ports are cut, what kl!ck users saw) and `block_diagnosis`, which says how a node is blocked: the address is banned, one port is cut, TCP is throttled, UDP is cut, one protocol is recognised, only whitelisted addresses pass on mobile, or nothing is blocked and the node is simply broken.

It answers with a short picture per protocol, node and group, then three to five ranked recommendations, each tied to its evidence and offered as one plan (a relay through an entry people reach, a move to port 443, a different protocol for a group, a CDN profile, the IPv6 entry, taking a blocked server out of subscriptions). The rules are fixed, so the same facts give the same answer; where a signal is missing it says so and names the check that would tell (for example `path_probe` over UDP from a node in the country). VLESS keeps no attempt counter, so its failures show only through the country view and kl!ck.

## Memory

- **Notes.** The assistant saves what it should remember ("Remembered: …" in the chat, with **Forget**). **AI → Memory** lists them: add, edit and delete any, or **Forget everything**. Every note goes into every conversation; at most 100 notes of 400 characters.
- **Notes for the assistant** (the owner's, in **AI → Assistant**): what every admin's assistant should know about this panel.
- **Conversations.** The assistant knows the titles of your recent conversations and can search and read the earlier ones when you refer to them. **AI → Memory → Conversations** opens one in the chat, or deletes one or all.

Conversations and notes are sealed in the panel's database and private to each admin: nobody else reads them, the owner included. Conversations are kept as long as **Keep conversations** says; notes stay until you delete them. Whatever looks like a key or a password is cut out of everything the panel sends to the provider, apart from the message you type yourself.

## Limits and cost

- One answer uses at most 24 tool calls and 10 minutes; **Stop** ends it at once and keeps what was said.
- One admin sends at most 20 messages a minute, and a conversation runs one answer at a time.
- Each provider has the owner's **Monthly budget, $**, estimated from the provider's prices and the usage each answer reports. At the budget the assistant stops until next month. **AI → Assistant → This month** shows the cost per admin.

## Safety

- The key is sealed with the panel's master key, entered by the owner after a passkey or code, and never shown, logged or written to the audit log.
- Names, notes and log lines from nodes and people are data to the assistant, never instructions; it has no tool that reaches the internet; the chat shows no images and no HTML, links to the admin's pages stay in the admin and other links open apart.
- Even if a node name or a note talked the model into proposing something, it would only be a card: the change happens when a person reads it and presses **Apply**.

## Follow-ups

The assistant answers your messages, and it can also come back by itself. When it starts or prepares something that finishes later (a debug capture of a node, a speed test, checks whose results come later, a rollout or a node update, a relay), it sets a follow-up: a time a little after the job ends and a note of what to read then. The conversation shows a quiet line such as "I'll come back with the result at 15:41 (in 9 min) · Cancel", in the panel's time zone.

- At that time the panel continues the same conversation on its own, with the browser closed too: the assistant sees the conversation so far, reads the result with its tools and answers. The answer is marked **On a timer**, and a dot on the chat button and in the list of conversations says it came while you were away.
- A follow-up for a change that starts only when you apply its plan (a debug capture, a speed test) waits for the apply and counts from it; if you dismiss the plan or let it expire, the follow-up goes too.
- It reads what your role may read, with the same limits and budget. Because nobody is watching, it prepares only the changes an operator could make without anyone's approval: for a change that needs an admin's rights or the owner's approval it tells you to ask in the chat. Nothing changes until you press **Apply** on a plan's card.
- At most 3 follow-ups wait in a conversation and 20 for an admin, each from 30 seconds to 24 hours ahead. **Cancel** on the line removes one; deleting the conversation removes its follow-ups.
- When a follow-up cannot run (the assistant is off, the month's budget is spent, the provider keeps failing), the conversation gets a short note with the reason. One the panel missed while it was down runs once when it starts again, unless it is more than a day late: then it is dropped with a note.
- If you ask for it ("tell me in Telegram if something is wrong"), a follow-up that finds a problem also sends one line to your linked Telegram chat. The request has to be in your own message; the assistant says in its answer whether a Telegram message will come.

## Scheduled tasks

The panel can check the fleet on its own, on a schedule, and tell you when it finds something: open **AI → Scheduled tasks**. The owner creates, changes and deletes tasks; helpers see them and may **Run now**; read-only admins do not see them.

### Two kinds

- **Check**: a list of the panel's own tools with their arguments, run in order, with no model and no cost: `fleet_digest`, `checks_run_now`, `block_diagnosis` for a country, `path_probe` between two nodes and the other read and probe tools of the operator profile. Each step may carry conditions on the tool's result; a condition that holds means the run found a problem. The editor offers the usual ones in one click: **A finding at warning or worse**, **A node is not online**, **A number above a threshold**, **A list is not empty**. A field is a dotted path into the result (`totals.nodes_problem_now`, `nodes[].status`, or `**.severity` for a field at any depth). **A failure of the tool is a problem too** counts a node that does not answer as a problem instead of a failed run.
- **Assistant**: a prompt, such as "Check the fleet and tell me whether something is wrong", given to the assistant on a schedule. It works with the operator's tools: it reads and probes and may prepare a plan, which waits for you as a card; it never applies anything. Its answers go into a conversation of the task's own, marked **Scheduled** in **AI → Memory** and in the chat's history, a new part per run; you can open it and go on from the last run. It ends every run with a verdict, which is what decides whether the run found a problem. Its cost comes from the assistant's monthly budget.

You can also ask the assistant ("check the fleet every morning and tell me if something is wrong"): it prepares the task as a plan that you approve. Outside agents do the same over [MCP](../reference/mcp.md) with `task_create_plan`.

### When it runs

- **Every** N minutes or hours (a check at most every 5 minutes, an assistant task every 15), **Daily** at a time, or **Weekly** on a day at a time, in the panel's time zone (the one of the update schedules; the tab names it).
- Changing the panel's fixed UTC offset recalculates the next run of enabled tasks. The panel does not follow daylight-saving time changes; set the offset again when local time changes.
- Each task starts up to a few minutes after its time, always by the same delay, so the tasks do not all start on the same second.
- One run of a task at a time. A run the panel missed while it was down runs once when it starts again, not once per missed time; a run cut short by a stop reads as interrupted.
- **Run now** starts one at once: at most once a minute for a check and once in five minutes for an assistant task.

### What a run says

| Status | Meaning |
|:--|:--|
| OK | Nothing to report. |
| Problem | A condition held, or the assistant's verdict says so. |
| Failed | The run could not do its work: a tool did not answer, the provider failed, no verdict; the reason is shown. |
| Skipped | The run did not start: the assistant is off, or this month's budget is spent. |

A problem opens the alert **Scheduled check found a problem** on **Health**, with the task's name and a short summary; it closes itself when a later run is clean. Three failed runs in a row open **Scheduled task keeps failing** with the reason. With **Also send to Telegram** on the task, both go to the chats linked for alerts. **Runs** under a task lists its last 50 runs (30 days at most): what each step matched and the start of the result, or the assistant's conversation and its cost.

### Reliability

- A provider that does not answer is asked again, with a growing wait; when it still fails, the run starts over on the **Fallback for scheduled tasks** you pick in **AI → Assistant**.
- The monthly budget of each provider holds: a run that would pass it does not start and says so.
- A check run takes at most 5 minutes, an assistant run at most 10 minutes on each provider; at most two checks and one assistant task run at the same time.
- A task holds no secret: a name, an argument or a prompt that looks like a key, a token or a link with a secret is refused, and the prompt is sealed in the panel's database like the assistant's notes.

## MCP, tokens and approvals

Outside agents (Claude Code, Cursor, your scripts) keep using [MCP](../reference/mcp.md) and [API tokens](../reference/api.md): they moved from Integrations to **AI → MCP and tokens**, and the plans waiting for the owner to **AI → Waiting for you**, with the badge in the menu. **Integrations** keeps the Telegram alerts.

## In the Cloudflare edition

The assistant is not available there yet, like MCP and the scheduled tasks: **AI → Assistant** and **AI → Scheduled tasks** say so. They will come together with MCP.

## When it does not answer

| The chat says | What to do |
|:--|:--|
| The provider refused the key. | Replace the key in **AI → Assistant**. |
| The provider account has no balance. | Top up the account at the provider. |
| This month's budget for the provider is spent. | Raise **Monthly budget, $**, or wait for next month. |
| The owner has not opened the assistant to your role. | The owner changes **Who may use it**. |
| Too many requests right now. | Wait a minute; the provider or the panel limits the rate. |
| The provider did not answer, or is overloaded. | Try again later, or switch the provider in **AI → Assistant**. |
