package protocols

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Secret handling is done by the framework, not by plugins: a schema property
// marked "x-secret": true is stored vault-encrypted outside settings_json and masked in the API.
const (
	// MaskedSecret is what the API shows instead of a stored secret, and what the client sends back to
	// say "keep the stored value".
	MaskedSecret = "••••"
	// GenerateSecret, like the empty string, asks for a freshly generated secret.
	GenerateSecret = "$generate"
)

// FlaggedPointers returns the JSON pointers of schema properties carrying the boolean flag
// (e.g. "x-secret", "x-critical"), sorted. Only "properties" of nested objects are walked.
func FlaggedPointers(schema []byte, flag string) ([]string, error) {
	var root map[string]any
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil, fmt.Errorf("protocols: bad schema: %w", err)
	}
	var out []string
	var walk func(node map[string]any, prefix string)
	walk = func(node map[string]any, prefix string) {
		props, _ := node["properties"].(map[string]any)
		for name, v := range props {
			child, ok := v.(map[string]any)
			if !ok {
				continue
			}
			ptr := prefix + "/" + escapePointer(name)
			if b, _ := child[flag].(bool); b {
				out = append(out, ptr)
			}
			walk(child, ptr)
		}
	}
	walk(root, "")
	sort.Strings(out)
	return out, nil
}

func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func unescapePointer(s string) string {
	return strings.NewReplacer("~1", "/", "~0", "~").Replace(s)
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep integers exact
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("settings must be a JSON object: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func encodeObject(m map[string]any) (json.RawMessage, error) {
	return json.Marshal(m) // map keys are sorted: stable bytes
}

func splitPointer(ptr string) []string {
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	for i := range parts {
		parts[i] = unescapePointer(parts[i])
	}
	return parts
}

func getPointer(m map[string]any, ptr string) (any, bool) {
	var cur any = m
	for _, k := range splitPointer(ptr) {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func setPointer(m map[string]any, ptr string, v any) {
	parts := splitPointer(ptr)
	cur := m
	for _, k := range parts[:len(parts)-1] {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = v
}

func deletePointer(m map[string]any, ptr string) {
	parts := splitPointer(ptr)
	cur := m
	for _, k := range parts[:len(parts)-1] {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, parts[len(parts)-1])
}

// deepMerge overlays over onto base: objects merge recursively, everything else is replaced.
func deepMerge(base, over map[string]any) {
	for k, v := range over {
		if ov, ok := v.(map[string]any); ok {
			if bv, ok := base[k].(map[string]any); ok {
				deepMerge(bv, ov)
				continue
			}
		}
		base[k] = v
	}
}

// SplitSecrets removes the secret values from merged settings. It returns the public document (safe for
// settings_json) and the removed values by pointer; only string secrets that are present are returned.
func SplitSecrets(merged json.RawMessage, secretPtrs []string) (json.RawMessage, map[string]string, error) {
	m, err := decodeObject(merged)
	if err != nil {
		return nil, nil, err
	}
	secrets := map[string]string{}
	for _, p := range secretPtrs {
		if v, ok := getPointer(m, p); ok {
			if s, ok := v.(string); ok {
				secrets[p] = s
			}
			deletePointer(m, p)
		}
	}
	pub, err := encodeObject(m)
	return pub, secrets, err
}

// MergeSecrets puts stored secrets back into the public document.
func MergeSecrets(public json.RawMessage, secrets map[string]string) (json.RawMessage, error) {
	m, err := decodeObject(public)
	if err != nil {
		return nil, err
	}
	for p, v := range secrets {
		setPointer(m, p, v)
	}
	return encodeObject(m)
}

// MaskSecrets replaces every secret of a merged document by MaskedSecret (for the API).
func MaskSecrets(merged json.RawMessage, secretPtrs []string) (json.RawMessage, error) {
	m, err := decodeObject(merged)
	if err != nil {
		return nil, err
	}
	for _, p := range secretPtrs {
		if _, ok := getPointer(m, p); ok {
			setPointer(m, p, MaskedSecret)
		}
	}
	return encodeObject(m)
}

// ResolveInput turns the settings document sent by a client into the merged settings to validate and
// store. base is what the client's document is laid over: the stored merged settings on update, or the
// plugin defaults on create (keys the client omits keep the base value, including generated secrets).
// Secret fields: MaskedSecret keeps base's value, "" or GenerateSecret takes a fresh value from fresh
// (a newly generated DefaultSettings document).
func ResolveInput(input, base, fresh json.RawMessage, secretPtrs []string) (json.RawMessage, error) {
	in, err := decodeObject(input)
	if err != nil {
		return nil, err
	}
	merged, err := decodeObject(base)
	if err != nil {
		return nil, err
	}
	orig, _ := decodeObject(base) // untouched copy: the mask must resolve to the base value
	f, err := decodeObject(fresh)
	if err != nil {
		return nil, err
	}
	deepMerge(merged, in)
	for _, p := range secretPtrs {
		v, ok := getPointer(merged, p)
		if !ok {
			continue
		}
		s, isStr := v.(string)
		if !isStr || (s != "" && s != GenerateSecret && s != MaskedSecret) {
			continue
		}
		if s == MaskedSecret {
			if ov, ok := getPointer(orig, p); ok {
				if os, ok := ov.(string); ok && os != MaskedSecret && os != "" {
					setPointer(merged, p, os)
					continue
				}
			}
		}
		if fv, ok := getPointer(f, p); ok {
			setPointer(merged, p, fv)
		}
	}
	return encodeObject(merged)
}

// ChangedPointers lists the given pointers whose value differs between two documents.
func ChangedPointers(a, b json.RawMessage, ptrs []string) []string {
	ma, err1 := decodeObject(a)
	mb, err2 := decodeObject(b)
	if err1 != nil || err2 != nil {
		return ptrs
	}
	var out []string
	for _, p := range ptrs {
		va, oka := getPointer(ma, p)
		vb, okb := getPointer(mb, p)
		ja, _ := json.Marshal(va)
		jb, _ := json.Marshal(vb)
		if oka != okb || !bytes.Equal(ja, jb) {
			out = append(out, p)
		}
	}
	return out
}
