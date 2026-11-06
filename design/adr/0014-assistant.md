# 0014. The assistant: a cloud model that runs the panel through its own MCP tools

Status: proposed, 2026-11-06.

## Context

The panel already has an agent surface: the MCP endpoint (`internal/panel/mcp`, [reference](../../docs/en/reference/mcp.md)).
Its tools are the panel's own procedures, called in process with the rights of an API token's profile; a change is a
`<tool>_plan` that describes it in the panel's words and an `_apply` that runs exactly that plan once; changes that touch
the fleet, many people or the user page wait for the owner, who approves with a passkey or an authenticator code; every
result passes a last scrub that removes anything shaped like a secret; text from users, nodes and other systems is marked
as untrusted data. To use it an owner needs an MCP client of their own (Claude Desktop, Claude Code, Cursor) on a computer.

The owner wants the same inside the panel: connect a cloud model with an API key, and talk to it in a chat that floats
over every page: ask about nodes, people, errors; set up subscriptions and groups; create and change anything, the
changes with a confirmation; and an assistant that remembers what it was told last time. The first providers are
Anthropic, DeepSeek, Z.ai and Ollama Cloud; others come later. MCP moves to the same page.

What must not happen: a key that leaks (to the browser, a log, the audit, another provider), panel secrets sent to a
provider, a model that changes something nobody confirmed, a prompt injected through a node name or a note that makes the
assistant act, a chat that reaches anything its human cannot.

## Decision

### One rule: the assistant is an MCP client of the panel, with the rights of the person who talks to it

- The panel runs the agent loop itself. Each turn opens an in-process MCP session (the go-sdk in-memory transport) to the
  panel's own MCP server, authenticated by a grant made for that turn: the admin who talks, acting with the MCP profile of
  their role (owner: Admin, operator: Operator, read-only: Readonly), source `assistant`. It sees exactly the tools an API
  token of that profile sees, with the same descriptions, scrub and untrusted-data marking. No tool, procedure or
  shortcut exists for the assistant alone, except the memory tools below.
- **The model never applies anything.** The `_apply` tools are not given to it. A plan it makes appears in the chat as a
  card: the panel's own summary, the facts (untrusted ones quoted), the danger notes, and **Apply** / **Cancel**. Only the
  human's click applies it, through `AssistantService.ApplyPlan`, which runs the apply with the confirm token on the
  server: the token goes neither to the model nor to the browser.
- A plan that needs the owner (`needs_approval`): when the owner is the one talking, the card asks for the passkey or the
  code and approves and applies in one step (the same `ApproveRequest` as in **Waiting for you**); when another admin
  talks, the card says it waits for the owner, and the plan is in **Waiting for you** as any agent's plan.
- Audit: every procedure is written as for MCP, with the actor "Assistant of <admin>" and the source `assistant`; plans and
  applies keep their rows. The conversation text is not in the audit.

### Providers

| Provider | Wire | Endpoint (fixed in code) | Models offered | Key check |
|---|---|---|---|---|
| Anthropic | Messages API, the official Go SDK (`anthropic-sdk-go/v2`) | `https://api.anthropic.com` | from `GET /v1/models`; default `claude-opus-5-5`, with Sonnet 5.5 and Haiku 5.5 named by price | `GET /v1/models` |
| DeepSeek | OpenAI Chat Completions | `https://api.deepseek.com` | from `GET /models` (context and output limits included); default `deepseek-flash` | `GET /models` |
| Z.ai | OpenAI Chat Completions | `https://api.z.ai/api/paas/v4` (the international platform, not the Coding Plan endpoints) | a list in code (no list endpoint): `glm-5.3`, the flash model | a one-token chat call |
| Ollama Cloud | OpenAI Chat Completions | `https://ollama.com/v1` | from `GET /v1/models`, only models that take tools | `GET /v1/models` |

- Two adapters behind one interface (`Stream(ctx, request) -> events`): the Anthropic one on the official SDK, and one
  small OpenAI-compatible client of our own for the other three (no SDK). The loop above them is provider-free: messages,
  tool calls, tool results, usage.
- Reasoning: Anthropic models think adaptively (`effort` from the owner's choice **Quick / Normal / Thorough**:
  low / medium / high; thinking blocks are passed back unchanged, the history is append-only); DeepSeek and Z.ai return
  `reasoning_content`, which is stored and sent back with its assistant message, as both require when tools are used.
  A refusal (`stop_reason: refusal` and the like) ends the turn with "The model declined this request".
- Tool choice is always `auto` (Z.ai and Ollama accept nothing else, Opus 5.5 rejects forced choice).
- Streaming: SSE from the providers, translated to one stream of chat events for the browser (text, tool started, tool
  done, plan card, memory saved, usage, done, error).
- No custom base URL in this phase: a free URL would be a way to send the key and the panel's data anywhere (and an SSRF
  path). An OpenAI-compatible "other provider" with an address is a later decision with its own guard.

### Keys and what leaves the panel

- A key is entered once, sealed with the panel vault (associated data `assistant-provider:<provider>`), never returned:
  the page shows the provider, the last four characters, when it was added and the last error. Adding, replacing and
  removing a key are owner-only, behind a fresh step-up, refused to API tokens; the audit row names the provider only.
- The panel talks to a provider only from the server (never the browser), over HTTPS with certificate checks, to the
  fixed address, without following a redirect to another host, with a size cap on every answer and timeouts.
- What a provider receives: the system prompt, the conversation, the tool definitions and the tool results. The results
  are what MCP returns (already scrubbed: no subscription links, keys, passwords, tokens, node addresses); the assistant's
  own context (memory, summaries, the page the admin is on) passes the same scrub. The browser warns before sending a
  message that looks like a key or a password.
- The provider card says where the data goes, from the provider's own policy: Anthropic (United States; API data is not
  used for training), Ollama Cloud (United States; not used for training), Z.ai (Singapore; the agreement says content is
  not stored), DeepSeek (China; the policy allows use for training). The owner picks knowing it.

### Prompt injection

- Names, notes, log lines, alert parameters stay marked untrusted (MCP already does it), and the system prompt says they are
  data. The defences that do not depend on the model obeying: it cannot apply (the human clicks); it has no tool that
  reaches the network or the outside (no fetch, no web search, no e-mail); it holds only the rights of the person.
- The chat renders Markdown without raw HTML and without images (an image URL is a way to send data out), links to the
  panel as panel links, other links as visible plain links that open in a new tab with `noreferrer`.
- A memory the model saves is shown in the chat ("Remembered: …", with Undo) and listed on the page: a planted memory is
  visible, and memory grants nothing.

### Memory

- **Notes** (per admin, at most 100 of 400 characters): the model saves and forgets them with `memory_save` and
  `memory_forget`; the admin adds, edits and deletes them on the page. All notes go into every conversation.
- **Panel notes for the assistant** (one text of the owner, up to 4000 characters): what the fleet is and how the owner
  wants it run ("nl1 is the test node", "never touch the group Family"). Every admin's assistant reads it.
- **Past conversations**: each conversation gets a title and a short summary written by the model when it goes idle or is
  compacted. The system prompt lists the last 20 titles; `conversation_search` (by words, over titles and summaries) and
  `conversation_read` (one past conversation, compacted) let the model look further back.
- **Long conversations** are compacted: past a share of the model's context, the older turns are replaced, from that point
  on, by a summary the model writes (a new segment; earlier requests are not edited, so thinking stays valid).
- Conversations and notes are sealed at rest (vault, associated data = their id), private to their admin (the owner can
  delete everyone's, not read them), kept 90 days by default (owner setting: 7 to 365, or until deleted).

### Limits

- Per turn: at most 24 tool calls, 120 s per model call, 10 minutes per turn; **Stop** ends a turn at once.
- Per admin: 20 turns a minute. Per provider: a monthly budget in US dollars set by the owner (estimated from the
  provider's price table and the usage each answer reports); at the budget the assistant stops with a clear message.
  The page shows the month's spend per provider and per admin.
- One turn at a time per conversation.

### Who uses it

The owner turns the assistant on and picks who may use it: **Only me** (default), **Admins** (owner and operators), or
**Everyone in the panel** (read-only admins too, with read tools only). Each acts with their own role.

### The page and the chat

- A new section **AI** in the navigation, with tabs:
  - **Assistant**: the four provider cards (connect, model, where the data goes, the month's spend), the default provider,
    **Quick / Normal / Thorough**, who may use it, the panel notes, retention, the monthly budget.
  - **Memory**: the admin's own notes and past conversations (open, delete, delete all).
  - **MCP**: what **Integrations → MCP** is today (address, client setup), with **Tokens**.
  - **Waiting for you**: the approvals, moved from Integrations (the badge moves with them).
  **Integrations** keeps Telegram. The old addresses redirect.
- The chat: a round button at the bottom corner of every admin page (and Ctrl/⌘+J) while the assistant is on for this
  admin; it opens a panel beside the page (full screen on a phone): the conversation with the answer streaming in, a line
  per tool call ("Read nodes · 12", expandable to the call), plan cards, "Remembered" chips, **Stop**, **New
  conversation**, the history. The page the admin is on goes with the message as a removable chip ("Node de1",
  "User anna", "Subscriptions → Who gets what").

### Editions

Both editions. On the Cloudflare edition the loop runs in the admin Worker, which streams the answer; the SDK and the
OpenAI client must fit the admin script's size budget (measured before the code lands; if the Anthropic SDK does not fit,
that adapter is written on raw HTTP for `js/wasm` only, with the same tests).

## Consequences

- The assistant can do no more than the person who talks to it, and nothing at all without their click. The plan/apply
  machinery, the approvals, the scrub and the audit of MCP carry it.
- The MCP tools become the product's main surface: what the assistant cannot do is what MCP cannot do. "Set up
  subscriptions and groups, create and change anything" needs tools MCP lacks today: groups (list, create, change,
  delete, membership), the subscription settings (rules, texts, the device limit, AmneziaWG in the link apps, the user
  page options), DNS presets, node settings (name, note, country, entries and the subscription switches), alerts and their
  muting, read access to events, health and the audit for the owner. They are added as MCP tools, with plans where they
  change something, so external MCP clients get them too.
- Panel data goes to a third party the owner chose; the provider card says where. Secrets never do.
- New tables (sealed): providers, conversations and their messages, notes, usage per month.

## Implementation plan

1. Go (Codex): the MCP tools listed above, with plans, policy entries and tests (in a first pass, before the assistant).
2. Go (Codex): `AssistantService` (proto), storage and migrations, the vault sealing, the two adapters with recorded
   fixtures (no network in tests), the loop (limits, Stop, compaction, budgets), the in-process MCP grant, the memory
   tools, `ApplyPlan`, audit, edge build. Tests: a fake provider that tries to apply, to call a missing tool, to loop, to
   inject through a tool result; a key that never appears in logs, audit, errors or responses; plans applied only by the
   click; another admin's conversation unreadable; budget and rate refusals; the scrub on everything sent.
3. Admin (Claude): the **AI** section and its tabs, the floating chat, strings in en, ru, zh, fa, tr, dev-kit states and
   screenshots.
4. Docs (Claude, en/ru/zh): a guide page for the assistant (providers, data, memory, safety), MCP moved, the reference.
