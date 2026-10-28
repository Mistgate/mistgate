// The facts relay_plan writes (internal/panel/mcp/tools_relay.go), in its own English, for the approval card's tests.
export const relayFacts = {
  create: [
    { key: "entry", value: "msk1", untrusted: true },
    { key: "exit", value: "de1", untrusted: true },
    { key: "server", value: "VLESS · XHTTP (vless, tcp 2053)", untrusted: true },
    { key: "entry_port", value: "chosen by the panel: the exit's port 2053 when it is free on the entry, else a free one", untrusted: false },
    { key: "exit_seen_from_inside", value: "partial, dark for MTS, Tele2", untrusted: true },
    { key: "entry_seen_from_inside", value: "ok", untrusted: true },
    { key: "entry_reaches_exit", value: "reach (tspu_row)", untrusted: false },
    { key: "warning", value: "the server has a hop range; the relay passes its one port only", untrusted: false },
    {
      key: "checks",
      value: "after it is made the entry tries the exit's server (leg), then the relay is switched on and a third node reaches the exit through the entry (path); if either fails the relay stays switched off",
      untrusted: false,
    },
    {
      key: "effect",
      value:
        'The entry node only sees who connects and how much, never contents or keys; accounting, limits and credentials stay on the exit. Once it is on, everyone who may use that server and may use the entry node gets a second server marked "via" and the entry\'s country in their subscription at its next refresh (every 12 hours by default); the servers they already use do not change.',
      untrusted: false,
    },
  ],
  move: [
    { key: "entry_port", value: "8443 -> 9443", untrusted: false },
    { key: "switched_on", value: "true", untrusted: false },
    { key: "last_check", value: "2026-10-28 20:05 UTC: leg ok, path fail (timeout)", untrusted: false },
    {
      key: "effect",
      value:
        'People who use the second server marked "via" need their subscription refreshed to get the new port. A relay that is on installs the new forward and the path is checked from outside; if that fails the relay is switched off.',
      untrusted: false,
    },
  ],
};
