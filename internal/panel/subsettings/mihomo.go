package subsettings

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The owner's own Mihomo YAML profiles (SubscriptionSettings.mihomo_templates): what a save takes, and how the panel puts the servers
// of a person into one. A template is written by an admin (or an agent the owner approved) and read by everyone of its groups, so
// the save is the trust boundary. Names of servers are hostile input: they enter the document as yaml.Node values with a quoted
// style, never as text.
//
// The panel touches three keys of the root: `proxies` (the person's servers go first), `proxy-groups` (an `Auto` group is added at
// the end while the switch of automatic selection is on and the template has no name `Auto`) and `dns` (added only when the template
// has none). The rest is encoded back as it was read, comments and anchors included.

// Limits enforced by Validate.
const (
	MaxMihomoTemplates      = 20
	MaxMihomoTemplateBytes  = 64 << 10
	MaxMihomoTemplatesBytes = 256 << 10
	MaxMihomoTemplateName   = 60
)

// AutoName is the group the panel adds over the servers of a person; a template refers to it by this name.
const AutoName = "Auto"

// CoreNames are the adapters of the Mihomo core: a proxy or group with one of these names would be shadowed by it.
var CoreNames = []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"}

// The codes of MihomoWarning.Code; the admin words them (web/src/i18n/subs.ts).
const (
	WarnNoServerGroup      = "no_server_group"      // no group takes the person's servers
	WarnOwnDNS             = "own_dns"              // the template has a dns section: the person's DNS preset is not applied
	WarnPresetRulesSkipped = "preset_rules_skipped" // the person's preset has rules (ads, split, countries) that a template does not get
	WarnOwnAuto            = "own_auto"             // the template has its own Auto: the panel adds none
	WarnUnknownRuleTarget  = "unknown_rule_target"  // Arg: the last field of a rule that no proxy, group or core name carries
	WarnUnused             = "unused"               // the template names no group: nobody gets it
)

const (
	maxRuleTargetWarnings = 10
	autoURL               = "https://www.gstatic.com/generate_204"
	trialServerCount      = 2
)

// MihomoWarning is one finding of a template that does not stop a save: the profile works, and the owner should look.
type MihomoWarning struct{ Code, Arg string }

// UniqueName makes a proxy name safe: not one of the core's adapter names (any case) and not taken yet. The name is added to taken.
func UniqueName(name string, taken map[string]bool) string {
	if name = strings.TrimSpace(name); name == "" {
		name = "server"
	}
	for slices.Contains(CoreNames, strings.ToUpper(name)) || taken[name] {
		name += " ·"
	}
	taken[name] = true
	return name
}

// SetProxyName replaces the name of a proxy mapping with a quoted scalar (the key exists: the caller found it).
func SetProxyName(m *yaml.Node, name string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "name" {
			m.Content[i+1] = quotedNode(name)
			return
		}
	}
}

func strNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
func quotedNode(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle}
}
func intNode(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(i)}
}
func seqOf(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}
func mapOf(kv ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: kv}
}
func quotedSeqOf(vs []string) *yaml.Node {
	n := seqOf()
	for _, v := range vs {
		n.Content = append(n.Content, quotedNode(v))
	}
	return n
}

// MihomoAutoGroup is the url-test group over members (the one definition of it: the built-in profile and a template both use it);
// interval is the seconds between its checks, 0 = the default.
func MihomoAutoGroup(name string, members []string, interval int) *yaml.Node {
	if interval <= 0 {
		interval = DefaultAutoInterval
	}
	return mapOf(
		strNode("name"), quotedNode(name), strNode("type"), strNode("url-test"), strNode("proxies"), quotedSeqOf(members),
		strNode("url"), quotedNode(autoURL), strNode("interval"), intNode(interval), strNode("tolerance"), intNode(50),
		strNode("lazy"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"}, strNode("timeout"), intNode(5000),
	)
}

// MihomoTemplateFor is the template of a group: the first that names it (the save allows one group in one template only).
func MihomoTemplateFor(s *adminv1.SubscriptionSettings, groupID string) *adminv1.MihomoTemplate {
	if groupID == "" {
		return nil
	}
	for _, t := range s.GetMihomoTemplates() {
		if slices.Contains(t.GetGroupIds(), groupID) {
			return t
		}
	}
	return nil
}

// ---- reading a template ----

// yamlErr is a YAML parser's message without its "yaml: " prefix (it names the line); the errors of a read into a value come as one
// line each, with the panel's name of the value for the Go type.
func yamlErr(err error) string {
	var te *yaml.TypeError
	if errors.As(err, &te) && len(te.Errors) > 0 {
		return typeNames.Replace(strings.Join(te.Errors, "; "))
	}
	return strings.TrimPrefix(err.Error(), "yaml: ")
}

var typeNames = strings.NewReplacer("subsettings.tplNamed", "a proxy", "subsettings.tplGroup", "a group", "[]subsettings.tplNamed", "the proxies", "[]subsettings.tplGroup", "the groups")

// parseDocument reads exactly one YAML document into nodes. An alias stays an alias here (nothing is expanded), so this alone does not
// catch an alias bomb or a repeated key: checkTemplate also decodes the whole text into a value, which does.
func parseDocument(text string) (doc, root *yaml.Node, err error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	doc = &yaml.Node{}
	if err := dec.Decode(doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, errors.New("no YAML document")
		}
		return nil, nil, errors.New(yamlErr(err))
	}
	var more yaml.Node
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("more than one YAML document (a second `---`)")
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 {
		return nil, nil, errors.New("no YAML document")
	}
	return doc, doc.Content[0], nil
}

// rootValue is the value of a key of the root mapping and the index of that value in root.Content; nil, -1 when the key is absent.
func rootValue(root *yaml.Node, key string) (*yaml.Node, int) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := root.Content[i]; k.Kind == yaml.ScalarNode && k.Value == key {
			return root.Content[i+1], i + 1
		}
	}
	return nil, -1
}

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" }

// checkRootShape is what the panel needs to change the document without touching more than it means to: a mapping at the root with no
// merge key, and `proxies` and `proxy-groups` that are lists of their own (not an alias, not an anchor an alias could reach), or null.
func checkRootShape(root *yaml.Node) error {
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: the root must be a mapping (key: value lines)", root.Line)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind == yaml.ScalarNode && (k.ShortTag() == "!!merge" || k.Value == "<<") && k.Style == 0 {
			return fmt.Errorf("line %d: a merge key `<<` at the root is not supported", k.Line)
		}
		if k.Kind != yaml.ScalarNode || (k.Value != "proxies" && k.Value != "proxy-groups") {
			continue
		}
		switch {
		case v.Kind == yaml.AliasNode:
			return fmt.Errorf("line %d: `%s` must be a list written out, not an alias", v.Line, k.Value)
		case v.Kind == yaml.SequenceNode && v.Anchor != "":
			return fmt.Errorf("line %d: `%s` must not carry an anchor (the panel changes this list)", v.Line, k.Value)
		case v.Kind != yaml.SequenceNode && !isNull(v):
			return fmt.Errorf("line %d: `%s` must be a list", v.Line, k.Value)
		}
	}
	return nil
}

type tplNamed struct {
	Name string `yaml:"name"`
}

type tplGroup struct {
	Name              string   `yaml:"name"`
	Type              string   `yaml:"type"`
	Proxies           []string `yaml:"proxies"`
	Use               []string `yaml:"use"`
	IncludeAllProxies bool     `yaml:"include-all-proxies"`
	IncludeAll        bool     `yaml:"include-all"`
}

// tplInfo is a template read for the keys the panel works on.
type tplInfo struct {
	doc, root *yaml.Node
	proxies   []tplNamed
	groups    []tplGroup
	names     map[string]bool // the names of the template's proxies and groups
}

// loadTemplate reads text as the panel changes it: one document, a mapping, the shape of checkRootShape, the entries of `proxies` and
// `proxy-groups` (anchors and `<<` inside them expanded) with names that are unique and not the core's.
func loadTemplate(text string) (*tplInfo, error) {
	doc, root, err := parseDocument(text)
	if err != nil {
		return nil, err
	}
	if err := checkRootShape(root); err != nil {
		return nil, err
	}
	var d struct {
		Proxies []tplNamed `yaml:"proxies"`
		Groups  []tplGroup `yaml:"proxy-groups"`
	}
	if err := root.Decode(&d); err != nil {
		return nil, errors.New(yamlErr(err))
	}
	t := &tplInfo{doc: doc, root: root, proxies: d.Proxies, groups: d.Groups, names: map[string]bool{}}
	add := func(what string, i int, name string) error {
		switch {
		case name == "":
			return fmt.Errorf("%s[%d]: a name is required", what, i)
		case slices.Contains(CoreNames, name):
			return fmt.Errorf("%s[%d]: %q is a name of the Mihomo core", what, i, name)
		case t.names[name]:
			return fmt.Errorf("%s[%d]: the name %q is used twice among the proxies and groups", what, i, name)
		}
		t.names[name] = true
		return nil
	}
	for i, p := range t.proxies {
		if err := add("proxies", i, p.Name); err != nil {
			return nil, err
		}
	}
	for i, g := range t.groups {
		if err := add("proxy-groups", i, g.Name); err != nil {
			return nil, err
		}
		if g.Type == "" {
			return nil, fmt.Errorf("proxy-groups[%d] %q: a type is required", i, g.Name)
		}
	}
	return t, nil
}

// checkRefs checks what the groups name: every member is a name of the template, of the core, or Auto (when the panel adds it), and
// every `use` is a key of proxy-providers. A name that is not there is why a Mihomo core refuses the whole file, and a name of a server
// is the usual one: only some people have it.
func (t *tplInfo) checkRefs(autoOn bool) error {
	var providers map[string]any
	loaded := false
	for i, g := range t.groups {
		for _, m := range g.Proxies {
			if t.names[m] || slices.Contains(CoreNames, m) || (autoOn && m == AutoName) {
				continue
			}
			if m == AutoName {
				return fmt.Errorf("proxy-groups[%d] %q: refers to Auto, which the panel adds only while automatic server selection is on (or define a group named Auto)", i, g.Name)
			}
			return fmt.Errorf("proxy-groups[%d] %q: proxies: %q is not a proxy or group of this template (take servers with include-all-proxies or filter)", i, g.Name, m)
		}
		for _, u := range g.Use {
			if !loaded {
				loaded = true
				if v, _ := rootValue(t.root, "proxy-providers"); v != nil {
					if err := v.Decode(&providers); err != nil && !isNull(v) {
						return errors.New("proxy-providers: " + yamlErr(err))
					}
				}
			}
			if _, ok := providers[u]; !ok {
				return fmt.Errorf("proxy-groups[%d] %q: use: %q is not in proxy-providers", i, g.Name, u)
			}
		}
	}
	return nil
}

// CheckMihomoTemplate is the save of one template's text, without its name and groups: it returns the first thing a save refuses. autoOn
// is link_auto_select of the document.
func CheckMihomoTemplate(text string, autoOn bool) error {
	switch {
	case text == "":
		return errors.New("the text is empty")
	case len(text) > MaxMihomoTemplateBytes:
		return fmt.Errorf("%d bytes, at most %d", len(text), MaxMihomoTemplateBytes)
	case !utf8.ValidString(text):
		return errors.New("the text is not valid UTF-8")
	}
	// The whole text into a value: this expands every alias (an alias bomb fails here: "excessive aliasing") and refuses a key given
	// twice, which a read into nodes, or into a struct of some of the keys, does not do. The second Decode is for a second document.
	dec := yaml.NewDecoder(strings.NewReader(text))
	var all any
	if err := dec.Decode(&all); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("no YAML document")
		}
		return errors.New(yamlErr(err))
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("more than one YAML document (a second `---`)")
	}
	t, err := loadTemplate(text)
	if err != nil {
		return err
	}
	if err := t.checkRefs(autoOn); err != nil {
		return err
	}
	// A trial build with two servers, as a person would get it: an error of the encoder is found here, not in the apps.
	out, err := ApplyMihomoTemplate(text, trialServers(), MihomoOptions{AutoSelect: autoOn})
	if err != nil {
		return fmt.Errorf("trial build: %w", err)
	}
	if _, _, err := parseDocument(string(out)); err != nil {
		return fmt.Errorf("trial build does not read back: %w", err)
	}
	return nil
}

func trialServers() []MihomoServer {
	out := make([]MihomoServer, trialServerCount)
	for i := range out {
		name := "server " + strconv.Itoa(i+1)
		out[i] = MihomoServer{Name: name, Node: mapOf(
			strNode("name"), quotedNode(name), strNode("type"), strNode("hysteria2"), strNode("server"), quotedNode("example.com"),
			strNode("port"), intNode(443), strNode("password"), quotedNode("x"))}
	}
	return out
}

// ---- building ----

// MihomoServer is one server of a person for a template: its proxy mapping (it has a `name` key) and the name it wants.
type MihomoServer struct {
	Node *yaml.Node
	Name string
}

// MihomoOptions are the choices of the document and of the person that apply to a template.
type MihomoOptions struct {
	// AutoSelect adds the Auto group (link_auto_select), unless the template has a name Auto.
	AutoSelect bool
	// AutoInterval is the seconds between the checks of the Auto group; 0 = the default.
	AutoInterval int
	// DNS is the dns section to add when the template has none (absent or null); nil adds nothing.
	DNS *yaml.Node
}

// ErrNoServers: a template is put around servers; without any there is nothing to put, and an empty url-test breaks the profile.
var ErrNoServers = errors.New("mihomo template: no servers")

// ApplyMihomoTemplate puts the servers of a person into a template: they go first in `proxies` (a name the template or the core has
// gets " ·" added, the template's names are never changed), then `Auto` and `dns` as described at the top of this file. The template
// is read anew each call (the stored document is shared: its nodes are not modified). The result is the document to send.
//
// ponytail: a read, a decode of the names and an encode on every request (about 6 ms of wasm CPU for a 14 KB template, 27 ms for
// 64 KiB). If that ever costs too much: remember the nodes per template text and copy them deeply.
func ApplyMihomoTemplate(text string, servers []MihomoServer, opt MihomoOptions) ([]byte, error) {
	if len(servers) == 0 {
		return nil, ErrNoServers
	}
	t, err := loadTemplate(text)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for n := range t.names {
		taken[n] = true
	}
	addAuto := opt.AutoSelect && !t.names[AutoName]
	if addAuto {
		taken[AutoName] = true
	}
	names := make([]string, 0, len(servers))
	nodes := make([]*yaml.Node, 0, len(servers))
	for _, s := range servers {
		name := UniqueName(s.Name, taken)
		SetProxyName(s.Node, name)
		names = append(names, name)
		nodes = append(nodes, s.Node)
	}

	// The key is added at the end of the root when the template lacks it (or has it null).
	list := func(key string) *yaml.Node {
		v, i := rootValue(t.root, key)
		if v != nil && v.Kind == yaml.SequenceNode {
			return v
		}
		seq := seqOf()
		if v != nil { // null: keep the key and its comments
			seq.HeadComment, seq.LineComment, seq.FootComment = v.HeadComment, v.LineComment, v.FootComment
			t.root.Content[i] = seq
		} else {
			t.root.Content = append(t.root.Content, strNode(key), seq)
		}
		return seq
	}
	px := list("proxies")
	px.Style = 0 // a flow list `[]` would take the servers on one line
	px.Content = append(nodes, px.Content...)
	if addAuto {
		g := list("proxy-groups")
		g.Style = 0
		g.Content = append(g.Content, MihomoAutoGroup(AutoName, names, opt.AutoInterval))
	}
	if opt.DNS != nil {
		if v, i := rootValue(t.root, "dns"); v == nil {
			t.root.Content = append(t.root.Content, strNode("dns"), opt.DNS)
		} else if isNull(v) {
			t.root.Content[i] = opt.DNS
		}
	}
	untagMerge(t.doc)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(t.doc); err != nil {
		return nil, fmt.Errorf("mihomo template: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("mihomo template: %w", err)
	}
	return buf.Bytes(), nil
}

// untagMerge makes the merge keys `<<` plain again: the encoder would write them as `!!merge <<`, which Mihomo reads but other
// readers of the same file may not. Aliases are not followed (their target is part of the tree already).
func untagMerge(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!merge" && n.Value == "<<" {
		n.Tag = ""
	}
	for _, c := range n.Content {
		untagMerge(c)
	}
}

// ---- warnings ----

// MihomoTemplateWarnings are what the owner should look at in a template that the save accepts (which codes: the Warn constants).
// preset_rules_skipped and unused need more than the text and are the caller's. A text that does not read gives none.
func MihomoTemplateWarnings(text string, autoOn bool) []MihomoWarning {
	t, err := loadTemplate(text)
	if err != nil {
		return nil
	}
	var out []MihomoWarning
	ownAuto := t.names[AutoName]
	takes := false
	for _, g := range t.groups {
		takes = takes || g.IncludeAllProxies || g.IncludeAll || (autoOn && !ownAuto && slices.Contains(g.Proxies, AutoName))
	}
	if !takes {
		out = append(out, MihomoWarning{Code: WarnNoServerGroup})
	}
	if v, _ := rootValue(t.root, "dns"); v != nil && !isNull(v) {
		out = append(out, MihomoWarning{Code: WarnOwnDNS})
	}
	if ownAuto && autoOn {
		out = append(out, MihomoWarning{Code: WarnOwnAuto})
	}
	var rules struct {
		Rules []any `yaml:"rules"`
	}
	_ = t.root.Decode(&rules) // `rules` is not a key the panel works on: a wrong shape is the Mihomo core's to say
	seen := map[string]bool{}
	for _, r := range rules.Rules {
		line, ok := r.(string)
		if !ok {
			continue
		}
		target := ruleTarget(line)
		if target == "" || seen[target] || t.names[target] || slices.Contains(CoreNames, target) || (autoOn && target == AutoName) {
			continue
		}
		seen[target] = true
		if len(seen) <= maxRuleTargetWarnings {
			out = append(out, MihomoWarning{Code: WarnUnknownRuleTarget, Arg: target})
		}
	}
	return out
}

// ruleTarget is the proxy or group a rule line sends to: the last field, not counting the `no-resolve` and `src` flags; "" for a line
// it cannot tell (SUB-RULE names a rule set, a lone MATCH has no target).
func ruleTarget(line string) string {
	parts := strings.Split(line, ",")
	if strings.EqualFold(strings.TrimSpace(parts[0]), "SUB-RULE") {
		return ""
	}
	i := len(parts) - 1
	for i > 1 {
		if f := strings.ToLower(strings.TrimSpace(parts[i])); f != "no-resolve" && f != "src" {
			break
		}
		i--
	}
	if i < 1 {
		return ""
	}
	return strings.TrimSpace(parts[i])
}

// ---- the document ----

// validateMihomoTemplates is the check of a save: the list, then each template's name, groups and text (CheckMihomoTemplate).
func validateMihomoTemplates(s *adminv1.SubscriptionSettings) error {
	list := s.GetMihomoTemplates()
	if len(list) > MaxMihomoTemplates {
		return invalid("mihomo_templates: at most %d", MaxMihomoTemplates)
	}
	total := 0
	names := map[string]bool{}
	owner := map[string]string{} // group id -> the template that has it
	for i, t := range list {
		w := fmt.Sprintf("mihomo_templates[%d]", i)
		if t == nil {
			return invalid("%s: missing", w)
		}
		if n := utf8.RuneCountInString(t.Name); t.Name == "" || n > MaxMihomoTemplateName || !utf8.ValidString(t.Name) || strings.ContainsFunc(t.Name, unicode.IsControl) {
			return invalid("%s.name: 1 to %d characters, no control characters", w, MaxMihomoTemplateName)
		}
		if key := strings.ToLower(t.Name); names[key] {
			return invalid("%s.name: %q is used by another template (names are compared without regard to case)", w, t.Name)
		} else {
			names[key] = true
		}
		if len(t.Yaml) > MaxMihomoTemplateBytes {
			return invalid("%s.yaml: %d bytes, at most %d", w, len(t.Yaml), MaxMihomoTemplateBytes)
		}
		if total += len(t.Yaml); total > MaxMihomoTemplatesBytes {
			return invalid("mihomo_templates: %d bytes of text in all, at most %d", total, MaxMihomoTemplatesBytes)
		}
		if len(t.GroupIds) > maxHeaderGroups {
			return invalid("%s.group_ids: at most %d", w, maxHeaderGroups)
		}
		own := map[string]bool{}
		for _, id := range t.GroupIds {
			if !idChars(id) || own[id] {
				return invalid("%s.group_ids: a repeated or malformed group id", w)
			}
			own[id] = true
			if other, ok := owner[id]; ok {
				return invalid("%s.group_ids: the group %q is already in the template %q (a group has one template)", w, id, other)
			}
			owner[id] = t.Name
		}
	}
	autoOn := LinkAutoSelect(s)
	for i, t := range list { // the parse of each is the expensive part: after every cheap check of all of them
		if err := CheckMihomoTemplate(t.Yaml, autoOn); err != nil {
			return invalid("mihomo_templates[%d] (%q).yaml: %v", i, t.Name, err)
		}
	}
	return nil
}

// normalizeMihomoTemplates trims the names (the text is kept as written).
func normalizeMihomoTemplates(list []*adminv1.MihomoTemplate) {
	for _, t := range list {
		if t != nil {
			t.Name = strings.TrimSpace(t.Name)
		}
	}
}
