# 0016. The ways as a graph: a node's entries, relays and exits in a node editor

Status: proposed, 2026-11-07.

## Context

**Ways in** shows one node as a metro line: people of a country at the top, a branch per way in (direct, through an entry
node, IPv6, a CDN), the node at the bottom, and a detail card for the open branch. It reads well for one node and one
hop. Relays make the picture span nodes: an entry node passes a port to a server of another node, the exit node's servers
leave directly or through WARP, and one entry can serve several exits. The list shows one side at a time, so an owner
who runs cascades switches between node pages to see how people actually get out.

The owner asked for a second view in the form of a node editor (as in shader and audio-patching tools): cards with
typed inputs and outputs, wires between them, and the ways managed on the canvas, "beautiful and convenient".

What the data allows today (see [`../../docs/en/guide/relays.md`](../../docs/en/guide/relays.md)):

- A **relay** is one hop: a port on the entry node (UDP or TCP, IPv4) forwarded to one server (`Inbound`) of another
  node. One relay per (server, entry); no relay of a relay. Created switched off; switching on needs a passing leg check,
  then a path check. Not for a CDN server, an ACME-IP certificate or AmneziaWG. Owner-only over `/api`; plan and apply
  over MCP.
- A server's **exit** is a property of the server (`Inbound.egress`: direct or WARP), changed only by making a twin
  profile with the other exit.
- **In subscriptions** (direct), the **IPv6 entry** and the server's port are switches and fields with their own RPCs.

## Decision

1. **A view switch on Ways in**: List | Graph (`?view=graph`), remembered per browser. The list stays the default and the
   only view on a phone-width screen; the graph is the same model drawn differently, not a second source of truth: both
   come from `lanesOf` (`web/src/lib/ways.ts`) plus the relays and the node's servers.

2. **What the graph shows**, around the open node, one hop each way:
   - **People · country** (the audience the panel checks for), on the left.
   - **Entry nodes** that relay to this node, each with one output per relay port.
   - **This node**: a row per server (protocol, network/port, state), an input on the left for the ways in and an
     output on the right for the way out; an **IPv6** row when the node has or can have one.
   - On an entry node, the **exit nodes** it relays to, with the relayed servers.
   - **Exits**: Internet (direct) and Cloudflare WARP when the node has it.
   - Wires: people → server (direct, coloured by the country verdict, drawn only for servers that are in subscriptions
     directly), people → entry node, entry → server (a relay: works, off, failing, being checked; traffic flows along
     a working one), server → exit.
   - Ports are typed by network: a relay wire joins UDP to UDP and TCP to TCP. A server that cannot be relayed has no
     relay input, and the reason shows on hover.

3. **What the graph edits**, with the existing RPCs and the same rules as the list (owner only; other roles get a
   read-only graph):
   - **Draw entry → server**: opens the existing relay dialog with both ends filled in; it creates, checks and switches
     on as today. The wire shows the check while it runs and stays dashed and off, with the failed stage, if a check fails.
   - **A relay wire's menu**: check, turn off / on, change the port, remove (with confirmation).
   - **People → server**: the **In subscriptions** switch of that server.
   - **IPv6 row**: opens the IPv6 entry dialog.
   - **Server → exit**: shown; changing it is an explicit "make a twin with the other exit" action, because it creates
     a profile rather than editing the wire.

   No new RPCs and no schema change.

4. **Layout**: columns by role (people, entries, node, exits), ordered to reduce crossings; the owner can drag cards,
   and positions are kept per admin and per node in the browser (`localStorage`). **Arrange** lays it out again.

5. **The editor is our own generic component** (`web/src/components/graph`), not a library: DOM cards, so the panel's
   own switches and buttons work inside them, and SVG wires measured from the ports. It goes further than the tools it
   is modelled on: touch and pinch, keyboard wiring (focus a port, Enter, pick a target, Enter), focusable cards and an
   announced change for screen readers, a minimap, auto-layout, a reason on an invalid target, reduced motion respected.
   The same component can later draw a fleet-wide cascade map.

6. **Out of scope** until a model exists, each its own decision: a relay of a relay (multi-hop chains), a server that
   leaves through another node, editing the whole fleet on one canvas, layouts saved on the panel.

7. **Next, on the same component, one question per graph** (a graph only where the links are the point of the screen,
   always with the list beside it):
   - **A person**: what they get and how. The person and their devices → their groups → the servers they are given
     (relay twins included) → the nodes → the exits; from the connection data, which server each device actually used.
     Editable: group membership (draw or drop the wire); the rest reads. It answers "why does it not work for them".
   - **The fleet** on the Nodes page: every node and every relay on one canvas, the cascade map. Read first; relay edits
     as on a node.
   - **Access**: groups → profiles → nodes. The most capable and the heaviest; only if the first ones prove useful.

## Consequences

- Cascades become visible in one place with their states, and the common edits (add a relay, switch it, drop a direct
  way from subscriptions) happen where the owner looks.
- Two views of one model: the mapping from lanes and relays to cards and wires is a pure function with tests, so the
  graph cannot show a way the list does not.
- The multi-call relay flow (create, check, switch on) keeps its current failure mode: a relay whose check fails stays,
  switched off, and says why. The graph draws exactly that.
- Positions in `localStorage` do not follow an admin to another browser; acceptable for a first version.
