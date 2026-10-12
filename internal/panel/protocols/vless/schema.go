package vless

// settingsSchema is the JSON Schema the admin UI renders the profile form from (vocabulary: the comment block of
// proto/mistgate/admin/v1/profile.proto).
//
// What is not here, on purpose: TLS security, WebSocket and the CDN fields need the node's TLS front. They are not offered,
// and Validate refuses them, so a profile can never ask a node for something its engine cannot serve. REALITY's target is
// the node's own site by default (target_kind self); target and server_names belong to the other choice, another site, and
// the editor hides them for self. The `security` setting exists (always "reality") but has no field: a control
// with one choice would only confuse; it returns to the schema with the second choice.
//
// Groups follow the form's vocabulary: basics (what the node listens on and where REALITY looks), obfuscation (what
// the handshake shows: names, short ids, fingerprint), advanced (XHTTP details, the exit). server_names and short_ids
// are lists of strings, which the generic form needs a "string-list" widget for. They and the XHTTP path have no default
// here: NormalizeSettings draws them per profile (the SPA keeps the defaults for a whole session).
const settingsSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "VLESS",
  "type": "object",
  "properties": {
    "port": {
      "type": "integer", "minimum": 1, "maximum": 65535, "default": 443,
      "title": "TCP port", "description": "Port the node listens on. Port 80 is kept for Let's Encrypt.",
      "x-group": "basics", "x-order": 10, "x-critical": true
    },
    "transport": {
      "type": "string", "enum": ["xhttp", "tcp"], "default": "xhttp",
      "x-enum-labels": {"xhttp": "XHTTP", "tcp": "TCP (RAW + Vision)"},
      "title": "Transport", "description": "XHTTP is the default. TCP with Vision is the fastest and has the least overhead.",
      "x-group": "basics", "x-widget": "segmented", "x-order": 20, "x-critical": true
    },
    "reality": {
      "type": "object", "title": "REALITY", "x-group": "basics", "x-order": 30,
      "properties": {
        "target_kind": {
          "type": "string", "enum": ["self", "site"], "default": "self",
          "x-enum-labels": {"self": "This node's site", "site": "Another site"},
          "title": "Site shown to everyone else",
          "description": "What anyone who is not a client sees on this port. This node's site is the node's own site with its own Let's Encrypt certificate (the node needs a domain). Another site is a real site you name, and the node passes visitors on to it.",
          "x-widget": "segmented", "x-order": 5, "x-critical": true
        },
        "target": {
          "type": "string", "minLength": 3, "title": "Target host:port",
          "description": "Another site's address, for example www.example.org:443. It must serve TLS 1.3 with X25519MLKEM768; a site in the same network (ASN) as the node is best.",
          "x-order": 10, "x-critical": true
        },
        "server_names": {
          "type": "array", "maxItems": 8, "uniqueItems": true,
          "items": {"type": "string", "pattern": "^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$"},
          "title": "Server names", "description": "Names the other site's certificate is valid for, up to 8. Empty = the host of the target. Needed when the target is an IP address.",
          "x-group": "obfuscation", "x-widget": "string-list", "x-order": 20, "x-critical": true
        },
        "short_ids": {
          "type": "array", "maxItems": 8, "uniqueItems": true,
          "items": {"type": "string", "pattern": "^([0-9A-Fa-f]{2}){0,8}$"},
          "title": "Short IDs", "description": "Hexadecimal, an even number of characters, up to 16, 1 to 8 values. Each device gets one of them. Empty: two are generated when the profile is saved.",
          "x-group": "obfuscation", "x-widget": "string-list", "x-order": 30, "x-critical": true
        },
        "fingerprint": {
          "type": "string", "enum": ["chrome", "firefox", "safari", "ios", "edge", "random"], "default": "chrome",
          "x-enum-labels": {"chrome": "Chrome", "firefox": "Firefox", "safari": "Safari", "ios": "iOS", "edge": "Edge", "random": "Random"},
          "title": "Client fingerprint", "description": "The browser the client's TLS handshake imitates.",
          "x-group": "obfuscation", "x-order": 40
        }
      }
    },
    "xhttp": {
      "type": "object", "title": "XHTTP", "x-group": "advanced", "x-order": 50,
      "properties": {
        "path": {
          "type": "string", "minLength": 1, "maxLength": 64, "pattern": "^/[A-Za-z0-9/_-]*$",
          "title": "Path", "description": "Starts with /, letters, digits and / _ -. Empty: a random one is made when the profile is saved.",
          "x-order": 10, "x-critical": true
        },
        "host": {
          "type": "string", "default": "", "title": "Host header", "description": "Empty = none.",
          "x-order": 20, "x-critical": true
        },
        "mode": {
          "type": "string", "enum": ["auto", "packet-up", "stream-up", "stream-one"], "default": "auto",
          "x-enum-labels": {"auto": "Auto", "packet-up": "Packet up", "stream-up": "Stream up", "stream-one": "Stream one"},
          "title": "Mode", "description": "The client's choice; the node accepts all of them.",
          "x-order": 30
        },
        "x_padding_bytes": {
          "type": "string", "pattern": "^[0-9]+-[0-9]+$", "default": "100-1000",
          "title": "Padding bytes", "description": "Random padding per request, min-max, 1 to 4096.",
          "x-order": 40, "x-critical": true
        }
      }
    },
    "egress": {
      "type": "string", "enum": ["direct", "warp"], "default": "direct",
      "x-enum-labels": {"direct": "Direct", "warp": "WARP"},
      "title": "Exit", "description": "WARP needs a WARP account on every node that serves this profile; without one the inbound does not start.",
      "x-group": "advanced", "x-widget": "segmented", "x-order": 60
    }
  }
}`
