package mtproto

// settingsSchema is the JSON Schema the admin UI renders the profile form from
// (vocabulary: see proto/mistgate/admin/v1/profile.proto). The name inside the Fake TLS
// handshake is a choice, like the target of VLESS REALITY: the node's own host name (the
// default) or another site the owner names, which also decides what a stranger on the port
// sees (the node's own site, or that site). The editor hides the domain unless the kind is
// another site (web/src/screens/profiles/mtproto-form.ts).
const settingsSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "MTProto",
  "type": "object",
  "properties": {
    "port": {
      "type": "integer", "minimum": 1, "maximum": 65535, "default": 443,
      "title": "TCP port", "description": "Port the node listens on for Telegram clients.",
      "x-group": "basics", "x-order": 10, "x-critical": true
    },
    "fake_tls": {
      "type": "object", "title": "Fake TLS", "x-group": "basics", "x-order": 20,
      "properties": {
        "kind": {
          "type": "string", "enum": ["own", "site"], "default": "own",
          "x-enum-labels": {"own": "This node's name", "site": "Another site"},
          "title": "Name in the TLS handshake",
          "description": "The domain Telegram puts in the handshake, and what anyone who is not a client sees on this port. This node's name is the node's own host name and its own site (the node needs a domain). Another site is a real site you name, and the node passes visitors on to it.",
          "x-widget": "segmented", "x-order": 5, "x-critical": true
        },
        "domain": {
          "type": "string", "maxLength": 253, "title": "Site",
          "description": "A host name such as www.google.com: no IP address, no port. A site that is fast and not blocked where your people are is best.",
          "x-order": 10, "x-critical": true
        }
      }
    }
  }
}`
