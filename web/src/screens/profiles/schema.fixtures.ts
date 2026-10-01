// Test fixtures: schemas as a protocol plugin would publish them.

// The schema the Hysteria2 plugin publishes (internal/panel/protocols/hysteria2/schema.go), trimmed to what
// the form reads.
export const hy2 = JSON.stringify({
  type: "object",
  properties: {
    port: { type: "integer", title: "UDP port", description: "Port the node listens on.", "x-group": "basics", "x-order": 10, "x-critical": true },
    hop: {
      type: "object",
      title: "Port hopping",
      "x-group": "basics",
      "x-widget": "port-range",
      "x-order": 20,
      "x-critical": true,
      properties: { from: { type: "integer" }, to: { type: "integer" } },
    },
    tls_mode: {
      type: "string",
      enum: ["acme_domain", "self_signed"],
      "x-enum-labels": { acme_domain: "Let's Encrypt", self_signed: "Self-signed (pinned)" },
      title: "Certificate",
      "x-group": "basics",
      "x-widget": "segmented",
      "x-order": 40,
    },
    obfs: {
      type: "object",
      title: "Obfuscation",
      "x-group": "obfuscation",
      "x-order": 50,
      properties: {
        type: { type: "string", enum: ["none", "salamander"], title: "Type", "x-widget": "segmented", "x-critical": true },
        password: { type: "string", title: "Password", "x-secret": true, "x-widget": "generate", "x-critical": true },
      },
    },
    masquerade: {
      type: "object",
      title: "Masquerade",
      description: "What the node answers to anything that is not a client.",
      "x-group": "advanced",
      "x-order": 60,
      properties: { type: { type: "string", enum: ["decoy", "none"], "x-enum-labels": { decoy: "Built-in site", none: "Nothing (404)" } } },
    },
    up_mbps: { type: "integer", title: "Upload ceiling", "x-unit": "Mbit/s", "x-group": "advanced", "x-order": 90 },
    udp: { type: "boolean", title: "Relay UDP", "x-group": "advanced", "x-order": 110 },
  },
});

// A made-up second protocol: no x-group on most fields, a secret without the generate widget, an enum with many
// values, a nested object, a field type the form cannot edit. It must render with no code written for it.
export const other = JSON.stringify({
  type: "object",
  properties: {
    mtu: { type: "integer", title: "MTU", "x-unit": "bytes", "x-order": 20 },
    dns: { type: "string", title: "DNS", "x-order": 10 },
    preset: { type: "string", enum: ["a", "b", "c", "d", "e"], title: "Preset", "x-order": 30 },
    junk: {
      type: "object",
      "x-group": "obfuscation",
      properties: { count: { type: "integer", title: "Jc" }, min: { type: "integer", title: "Jmin" } },
    },
    key: { type: "string", title: "Private key", "x-secret": true, "x-group": "basics" },
    peers: { type: "array", title: "Peers" },
    ratio: { type: "number", title: "Ratio" },
  },
});
