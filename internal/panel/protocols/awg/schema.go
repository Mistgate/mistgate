package awg

// settingsSchema is the JSON Schema the admin UI renders the profile form from (vocabulary: the comment block
// of proto/mistgate/admin/v1/profile.proto). x-critical marks what must match byte for byte between node and
// client, so a change breaks the configs already issued: version, port, S1-S4,
// H1-H4, the header protection key, random trailers; and the subnets, which never change after first use.
// x-secret is the header protection key. The property names equal the JSON tags of Settings and the enums equal
// mimicry.Presets(); awg_test.go keeps both in step.
//
// A `default` is given only where it is the same for every profile. The values that are random per profile (junk,
// S, H, timers, keepalive) have none: DefaultSettings draws them, and a profile created without an obfuscation
// block gets a generated one (NormalizeSettings), so no fixed number here ever becomes a live config.
const settingsSchema = `{
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
}`
