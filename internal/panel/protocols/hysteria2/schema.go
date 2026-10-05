package hysteria2

// settingsSchema is the JSON Schema the admin UI renders the profile form from. Vocabulary: see the
// comment block in proto/mistgate/admin/v1/profile.proto. x-enum-labels is an object {value: label}.
// Deliberately absent from the enums: tls_mode "acme_ip" (Validate rejects it
// explicitly) and masquerade proxy/file/string; they become selectable when the node side supports them.
// The node engine (internal/node/hysteria2) reads exactly these values; the cross-module test in that package
// keeps the two in step.
const settingsSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Hysteria2",
  "type": "object",
  "properties": {
    "port": {
      "type": "integer", "minimum": 1, "maximum": 65535, "default": 443,
      "title": "UDP port", "description": "Port the node listens on.",
      "x-group": "basics", "x-order": 10, "x-critical": true
    },
    "hop": {
      "type": "object", "title": "Port hopping",
      "description": "UDP range the node forwards to the port; 0 and 0 = off. From 1024, at most 20000 ports.",
      "x-group": "basics", "x-widget": "port-range", "x-order": 20, "x-critical": true,
      "properties": {
        "from": {"type": "integer", "minimum": 0, "maximum": 65535, "default": 0, "title": "From"},
        "to": {"type": "integer", "minimum": 0, "maximum": 65535, "default": 0, "title": "To"}
      }
    },
    "sni": {
      "type": "string", "default": "", "title": "SNI override",
      "description": "Server name clients use. Empty = the node address.",
      "x-group": "basics", "x-order": 30, "x-critical": true
    },
    "tls_mode": {
      "type": "string", "enum": ["acme_domain", "self_signed"], "default": "acme_domain",
      "x-enum-labels": {"acme_domain": "Let's Encrypt", "self_signed": "Self-signed (pinned)"},
      "title": "Certificate", "x-group": "basics", "x-widget": "segmented", "x-order": 40, "x-critical": true
    },
    "obfs": {
      "type": "object", "title": "Obfuscation", "x-group": "obfuscation", "x-order": 50,
      "properties": {
        "type": {
          "type": "string", "enum": ["none", "salamander", "gecko"], "default": "salamander",
          "x-enum-labels": {"none": "None", "salamander": "Salamander", "gecko": "Gecko (experimental)"},
          "title": "Type", "description": "Salamander hides the QUIC handshake. Gecko reaches only Mihomo apps (kl!ck, Clash Verge, FlClash): Happ and the other link-list apps do not speak it, so they do not get these servers.",
          "x-widget": "segmented", "x-critical": true
        },
        "password": {
          "type": "string", "minLength": 8, "maxLength": 128, "title": "Password",
          "x-secret": true, "x-widget": "generate", "x-critical": true
        }
      }
    },
    "masquerade": {
      "type": "object", "title": "Masquerade", "x-group": "advanced", "x-order": 60,
      "description": "What the node answers to anything that is not a Hysteria2 client.",
      "properties": {
        "type": {
          "type": "string", "enum": ["decoy", "none"], "default": "decoy",
          "x-enum-labels": {"decoy": "Built-in site", "none": "Nothing (404)"},
          "title": "Type", "x-widget": "segmented"
        }
      }
    },
    "ignore_client_bandwidth": {
      "type": "boolean", "default": true, "title": "Ignore the client-declared speed (always BBR)",
      "description": "Otherwise a client can declare a high speed (Brutal) and take the whole node link; BBR shares it fairly.",
      "x-title-ru": "Не доверять заявленной скорости клиента (всегда BBR)",
      "x-description-ru": "Иначе клиент может заявить высокую скорость (Brutal) и занять весь канал ноды; BBR делит канал поровну",
      "x-group": "advanced", "x-order": 70
    },
    "bbr_profile": {
      "type": "string", "enum": ["standard", "conservative", "aggressive"], "default": "standard",
      "x-enum-labels": {"standard": "Standard", "conservative": "Conservative", "aggressive": "Aggressive"},
      "title": "BBR profile", "x-group": "advanced", "x-widget": "segmented", "x-order": 80
    },
    "up_mbps": {
      "type": "integer", "minimum": 0, "maximum": 100000, "default": 0, "x-unit": "Mbit/s",
      "title": "Upload ceiling", "description": "0 = unlimited.", "x-group": "advanced", "x-order": 90
    },
    "down_mbps": {
      "type": "integer", "minimum": 0, "maximum": 100000, "default": 0, "x-unit": "Mbit/s",
      "title": "Download ceiling", "description": "0 = unlimited.", "x-group": "advanced", "x-order": 100
    },
    "udp": {
      "type": "boolean", "default": true, "title": "Relay UDP",
      "description": "Off = clients can tunnel TCP only.", "x-group": "advanced", "x-order": 110
    },
    "egress": {
      "type": "string", "enum": ["direct", "warp"], "default": "direct",
      "x-enum-labels": {"direct": "Direct", "warp": "WARP"},
      "title": "Exit", "description": "WARP needs a WARP account on every node that serves this profile; without one the inbound does not start.",
      "x-group": "advanced", "x-widget": "segmented", "x-order": 120
    }
  }
}`
