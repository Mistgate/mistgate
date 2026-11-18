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

// The schema the VLESS plugin publishes (internal/panel/protocols/vless/schema.go), whole: the REALITY block with two string
// lists, the TLS and CDN fields, the XHTTP and WebSocket blocks that only their transports use.
export const vless = String.raw`{
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
      "type": "string", "enum": ["xhttp", "tcp", "ws"], "default": "xhttp",
      "x-enum-labels": {"xhttp": "XHTTP", "tcp": "TCP (RAW + Vision)", "ws": "WebSocket"},
      "title": "Transport", "description": "XHTTP is the default. TCP with Vision is the fastest and has the least overhead.",
      "x-group": "basics", "x-widget": "segmented", "x-order": 20, "x-critical": true
    },
    "security": {
      "type": "string", "enum": ["reality", "tls"], "default": "reality",
      "x-enum-labels": {"reality": "REALITY", "tls": "TLS"},
      "title": "Security", "description": "REALITY is direct. TLS works directly with a domain certificate or through a CDN.",
      "x-group": "basics", "x-widget": "segmented", "x-order": 25, "x-critical": true
    },
    "tls_mode": {
      "type": "string", "enum": ["acme_domain", "self_signed"],
      "x-enum-labels": {"acme_domain": "Let's Encrypt", "self_signed": "Self-signed origin"},
      "title": "Origin certificate", "description": "A direct TLS connection needs a domain certificate. A self-signed certificate is allowed only when a CDN connects to the origin.",
      "x-group": "tls", "x-order": 10, "x-critical": true
    },
    "sni": {
      "type": "string", "default": "", "title": "Origin server name", "description": "Empty uses the node's domain or the inbound override.",
      "x-group": "tls", "x-order": 20, "x-critical": true
    },
    "cdn": {
      "type": "object", "title": "CDN", "x-group": "tls", "x-order": 30,
      "properties": {
        "host": {"type": "string", "default": "", "title": "CDN host", "description": "The host clients connect to. The client Host and SNI use this value.", "x-order": 10, "x-critical": true},
        "port": {"type": "integer", "minimum": 1, "maximum": 65535, "default": 443, "title": "CDN port", "x-order": 20}
      }
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
          "type": "string", "default": "", "title": "Host header", "description": "REALITY only. TLS derives the client Host from the origin SNI or CDN host.",
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
    "ws": {
      "type": "object", "title": "WebSocket", "x-group": "advanced", "x-order": 55,
      "properties": {
        "path": {"type": "string", "minLength": 1, "maxLength": 64, "pattern": "^/[A-Za-z0-9/_-]*$", "title": "Path", "description": "Empty: a random one is made when the profile is saved.", "x-order": 10, "x-critical": true},
        "host": {"type": "string", "default": "", "title": "Client Host header", "description": "The node's WebSocket origin always leaves Host empty. Empty = no client Host override.", "x-order": 20, "x-critical": true}
      }
    },
    "egress": {
      "type": "string", "enum": ["direct", "warp"], "default": "direct",
      "x-enum-labels": {"direct": "Direct", "warp": "WARP"},
      "title": "Exit", "description": "WARP needs a WARP account on every node that serves this profile; without one the inbound does not start.",
      "x-group": "advanced", "x-widget": "segmented", "x-order": 60
    }
  }
}`;

/** The settings schema of the TrustTunnel plugin (internal/panel/protocols/trusttunnel/schema.go), as the panel sends it. */
export const trusttunnel = `{
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
}`;

/** AmneziaWG's schema as the panel serves it (internal/panel/protocols/awg/schema.go): the page and its scenes draw their cards from it. */
export const awg = String.raw`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "AmneziaWG",
  "type": "object",
  "properties": {
    "version": {
      "type": "string", "enum": ["3.1", "2.0"], "default": "3.1",
      "x-enum-labels": {"3.1": "AmneziaWG 3.1", "2.0": "AmneziaWG 2.0 (older clients)"},
      "title": "Protocol version",
      "description": "2.0 is for AmneziaVPN below 5.0.1.5 and for Mihomo below 1.19.30. One profile is one version: a user who needs both gets two devices.",
      "x-group": "basics", "x-widget": "segmented", "x-order": 10, "x-critical": true
    },
    "port": {
      "type": "integer", "minimum": 1, "maximum": 65535,
      "title": "UDP port", "description": "Port the node listens on. A random one in 10000-60000 is chosen for a new profile.",
      "x-group": "basics", "x-order": 20, "x-critical": true
    },
    "mtu": {
      "type": "integer", "minimum": 1200, "maximum": 1420, "default": 1280, "x-unit": "bytes",
      "title": "MTU", "description": "Tunnel MTU on the node and in the .conf. MTU + S4 + 80 must fit 1500. Desktop AmneziaVPN overwrites it when a vpn:// key is imported.",
      "x-group": "basics", "x-order": 30
    },
    "egress": {
      "type": "string", "enum": ["direct", "warp"], "default": "direct",
      "x-enum-labels": {"direct": "Direct", "warp": "WARP"},
      "title": "Exit", "description": "WARP needs a WARP account on every node that serves this profile.",
      "x-group": "basics", "x-widget": "segmented", "x-order": 40
    },
    "subnet4": {
      "type": "string", "pattern": "^[0-9]{1,3}(\\.[0-9]{1,3}){3}/[0-9]{2}$",
      "title": "Client network (IPv4)", "description": "The node is .1, every device gets one address. Read-only once the profile has a device.",
      "x-group": "basics", "x-order": 50, "x-critical": true
    },
    "subnet6": {
      "type": "string", "default": "",
      "title": "Client network (IPv6)", "description": "A /64 from fc00::/7; empty = no IPv6 inside the tunnel. Read-only once the profile has a device.",
      "x-group": "basics", "x-order": 60, "x-critical": true
    },
    "obfuscation": {
      "type": "object", "title": "Obfuscation", "x-group": "obfuscation", "x-order": 70,
      "properties": {
        "preset": {
          "type": "string", "default": "dns",
          "enum": ["quic", "curl_quic", "dns", "stun", "webrtc", "sip", "ntp", "rtp", "ssdp", "dtls", "custom"],
          "x-enum-labels": {"quic": "QUIC", "curl_quic": "QUIC (curl)", "dns": "DNS", "stun": "STUN", "webrtc": "WebRTC", "sip": "SIP", "ntp": "NTP", "rtp": "RTP", "ssdp": "SSDP", "dtls": "DTLS", "custom": "Custom"},
          "title": "Mimicry", "description": "What the first packets look like. Picking one fills I1-I5 only; Generate fills everything. Custom keeps the packets you write.",
          "x-order": 10
        },
        "domain": {
          "type": "string", "default": "", "maxLength": 100,
          "title": "Domain in the packets", "description": "The host name QUIC (SNI), DNS and SIP packets carry. Empty = a random one from a built-in list.",
          "x-order": 15
        },
        "jc": {"type": "integer", "minimum": 0, "maximum": 128, "title": "Junk packets (Jc)", "description": "Sent by the client before each handshake. 4-12 is usual.", "x-order": 20},
        "jmin": {"type": "integer", "minimum": 0, "maximum": 1280, "x-unit": "bytes", "title": "Junk size, min (Jmin)", "x-order": 30},
        "jmax": {"type": "integer", "minimum": 0, "maximum": 1280, "x-unit": "bytes", "title": "Junk size, max (Jmax)", "description": "Keep it below the MTU.", "x-order": 40},
        "s1": {"type": "integer", "minimum": 0, "maximum": 150, "x-unit": "bytes", "title": "Init padding (S1)", "x-order": 50, "x-critical": true},
        "s2": {"type": "integer", "minimum": 0, "maximum": 150, "x-unit": "bytes", "title": "Response padding (S2)", "x-order": 60, "x-critical": true},
        "s3": {"type": "integer", "minimum": 0, "maximum": 64, "x-unit": "bytes", "title": "Cookie padding (S3)", "x-order": 70, "x-critical": true},
        "s4": {"type": "integer", "minimum": 0, "maximum": 64, "x-unit": "bytes", "title": "Data padding (S4)", "description": "Added to every data packet: keep it small, MTU + S4 + 80 must fit 1500.", "x-order": 80, "x-critical": true},
        "h1": {"type": "string", "pattern": "^[0-9]{1,10}(-[0-9]{1,10})?$", "title": "Init header (H1)", "description": "A number or a range lo-hi. 1, 2, 3, 4 are the headers of plain WireGuard: only fine with a header protection key.", "x-order": 90, "x-critical": true},
        "h2": {"type": "string", "pattern": "^[0-9]{1,10}(-[0-9]{1,10})?$", "title": "Response header (H2)", "x-order": 100, "x-critical": true},
        "h3": {"type": "string", "pattern": "^[0-9]{1,10}(-[0-9]{1,10})?$", "title": "Cookie header (H3)", "x-order": 110, "x-critical": true},
        "h4": {"type": "string", "pattern": "^[0-9]{1,10}(-[0-9]{1,10})?$", "title": "Data header (H4)", "description": "The four ranges must not overlap.", "x-order": 120, "x-critical": true},
        "i1": {"type": "string", "default": "", "maxLength": 3500, "title": "Signature packet 1 (I1)", "description": "Tags b, r, rc, rd, t. Sent by the client before each handshake; the node ignores it. With per-device signatures and a look that varies, every device gets its own packets from the look and the domain: what is written here is shown in the preview only. Pick Custom to send these.", "x-order": 130},
        "per_device_signature": {"type": "boolean", "default": true, "title": "A signature of its own for every device", "description": "Each device gets different I1-I5 from the preset, the same ones every time its config is shown. Off: all devices share I1-I5. Not for the custom preset.", "x-order": 135},
        "i2": {"type": "string", "default": "", "maxLength": 3500, "title": "Signature packet 2 (I2)", "x-group": "advanced", "x-order": 140},
        "i3": {"type": "string", "default": "", "maxLength": 3500, "title": "Signature packet 3 (I3)", "x-group": "advanced", "x-order": 150},
        "i4": {"type": "string", "default": "", "maxLength": 3500, "title": "Signature packet 4 (I4)", "x-group": "advanced", "x-order": 160},
        "i5": {"type": "string", "default": "", "maxLength": 3500, "title": "Signature packet 5 (I5)", "x-group": "advanced", "x-order": 170},
        "signature_seed": {"type": "string", "default": "", "maxLength": 64, "title": "Signature seed", "description": "Not a secret. With the device id it makes the per-device packets reproducible; a new one changes every device's packets the next time its config is shown.", "x-group": "advanced", "x-order": 175},
        "header_protection_key": {
          "type": "string", "title": "Header protection key", "description": "3.1 only. The same 32 bytes on the node and in every client.",
          "x-secret": true, "x-widget": "generate", "x-group": "advanced", "x-order": 180, "x-critical": true
        },
        "random_trailers": {"type": "boolean", "default": true, "title": "Random trailers", "description": "3.1 only. Must match on both sides.", "x-group": "advanced", "x-order": 190, "x-critical": true},
        "disable_cookies": {"type": "boolean", "default": false, "title": "Disable cookie replies", "description": "3.1 only, node side. On removes the protection against handshake floods.", "x-group": "advanced", "x-order": 200},
        "content_padding_addition": {"type": "string", "pattern": "^([0-9]{1,5}(-[0-9]{1,5})?)?$", "x-unit": "bytes", "title": "Extra padding", "description": "3.1 only. A range of random bytes added to data packets, never above the largest packet seen.", "x-group": "advanced", "x-order": 210},
        "rekey_after_time": {"type": "string", "pattern": "^([0-9]{1,5}(-[0-9]{1,5})?)?$", "x-unit": "s", "title": "Rekey after", "description": "3.1 only.", "x-group": "advanced", "x-order": 220},
        "rekey_timeout": {"type": "string", "pattern": "^([0-9]{1,5}(-[0-9]{1,5})?)?$", "x-unit": "s", "title": "Rekey timeout", "description": "3.1 only.", "x-group": "advanced", "x-order": 230},
        "reject_after_time": {"type": "string", "pattern": "^([0-9]{1,5}(-[0-9]{1,5})?)?$", "x-unit": "s", "title": "Reject after", "description": "3.1 only.", "x-group": "advanced", "x-order": 240},
        "keepalive_timeout": {"type": "string", "pattern": "^([0-9]{1,5}(-[0-9]{1,5})?)?$", "x-unit": "s", "title": "Keepalive timeout", "description": "3.1 only.", "x-group": "advanced", "x-order": 250},
        "max_handshake_attempts": {"type": "string", "pattern": "^([0-9]{1,5}(-[0-9]{1,5})?)?$", "title": "Handshake attempts", "description": "3.1 only.", "x-group": "advanced", "x-order": 260},
        "persistent_keepalive": {"type": "string", "pattern": "^([0-9]{1,5}(-[0-9]{1,5})?)?$", "x-unit": "s", "title": "Persistent keepalive", "description": "Written into the client; keep it under 30 s, the UDP timeout of a NAT. 2.0 takes one number; Mihomo uses the first one.", "x-group": "advanced", "x-order": 270}
      }
    }
  }
}`;
