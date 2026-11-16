package trusttunnel

const settingsSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "TrustTunnel",
  "type": "object",
  "properties": {
    "port": {
      "type": "integer", "minimum": 1, "maximum": 65535, "default": 443,
      "title": "TCP port", "description": "Port the node listens on for HTTP/2.",
      "x-group": "basics", "x-order": 10, "x-critical": true
    },
    "h3": {
      "type": "boolean", "default": false, "title": "HTTP/3",
      "description": "Also listen on UDP at the same port. Disabled by default because UDP 443 is commonly used by Hysteria2.",
      "x-group": "basics", "x-order": 20, "x-critical": true
    },
    "sni": {
      "type": "string", "maxLength": 253, "default": "", "title": "SNI override",
      "description": "Certificate name clients use. Empty = the node address.",
      "x-group": "basics", "x-order": 30, "x-critical": true
    },
    "tls_mode": {
      "type": "string", "enum": ["acme_domain", "self_signed"], "default": "acme_domain",
      "x-enum-labels": {"acme_domain": "Let's Encrypt", "self_signed": "Self-signed (pinned)"},
      "title": "Certificate", "x-group": "basics", "x-widget": "segmented", "x-order": 40, "x-critical": true
    },
    "egress": {
      "type": "string", "enum": ["direct", "warp"], "default": "direct",
      "x-enum-labels": {"direct": "Direct", "warp": "WARP"},
      "title": "Exit", "description": "How the node reaches destination servers.",
      "x-group": "advanced", "x-widget": "segmented", "x-order": 50, "x-critical": true
    }
  }
}`
