package mtproto

// settingsSchema is the JSON Schema the admin UI renders the profile form from
// (vocabulary: see proto/mistgate/admin/v1/profile.proto). Fake TLS with a
// fixed SNI is always on in the first version: the per-credential secret is the
// ee link, and one key works on every MTProto profile of the user's groups.
const settingsSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "MTProto",
  "type": "object",
  "properties": {
    "port": {
      "type": "integer", "minimum": 1, "maximum": 65535, "default": 443,
      "title": "TCP port", "description": "Port the node listens on for Telegram clients.",
      "x-group": "basics", "x-order": 10, "x-critical": true
    }
  }
}`
