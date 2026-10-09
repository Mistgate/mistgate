//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ufwInboundCommentPrefix    = "mistgate-node-managed-udp-v1-"
	ufwInboundTCPCommentPrefix = "mistgate-node-managed-tcp-v1-"
	defaultUFWConf             = "/etc/ufw/ufw.conf"
	// firewallCmdTimeout bounds every host firewall command: a hung ufw or a held xtables lock must not stall the
	// agent's single apply worker. A ufw run outside the sandbox is also stopped by systemd a little earlier.
	firewallCmdTimeout = 30 * time.Second
	// firewallRetry is how often an unchanged desired set is retried after a failure (a success is not repeated).
	firewallRetry = 5 * time.Minute
)

// ufwSimpleInboundRule is a rule `ufw show added` prints for a simple UDP/TCP port rule. UFW treats it as the same rule
// as ours whatever its action or comment, so adding ours over it would rewrite the owner's rule ("Rule updated").
var ufwSimpleInboundRule = regexp.MustCompile(`^ufw (allow|deny|reject|limit)(?: in)?(?: log| log-all)? (\d+(?::\d+)?)/(udp|tcp)(?: comment .*)?$`)

// SyncInboundPorts manages only UFW rules carrying Mistgate's private comment tags, and never adds one over a rule
// for the same protocol and port that is not ours. It leaves an inactive firewall untouched. firewalld is only checked,
// never edited: its CLI has no runtime-only, per-rule ownership marker, and activating a custom service requires a
// global reload that would discard unrelated runtime-only changes.
//
// An unchanged desired set with an unchanged UFW state does nothing after a success and retries a failure at most
// every firewallRetry: the agent calls this on every reconcile, including the periodic sweep.
func (h *linuxHost) SyncInboundPorts(ctx context.Context, udp []UDPInboundPort, tcp []uint16) error {
	desiredUDP, err := normalizeUDPInboundPorts(udp)
	if err != nil {
		return err
	}
	desiredTCP, err := normalizeTCPInboundPorts(tcp)
	if err != nil {
		return err
	}
	h.fw.Lock()
	defer h.fw.Unlock()

	ufwOn := ufwEnabled(h.ufwConf)
	key := fmt.Sprint(ufwOn, desiredUDP, desiredTCP)
	if h.inboundSynced && h.inboundKey == key && (h.inboundErr == nil || time.Since(h.inboundAt) < firewallRetry) {
		return h.inboundErr
	}
	var errs []error
	if ufwOn {
		if err := h.syncUFWInboundPorts(ctx, desiredUDP, desiredTCP); err != nil {
			errs = append(errs, err)
		}
	}
	if len(desiredUDP) > 0 {
		if err := h.checkFirewalldInboundPorts(ctx, "udp", desiredUDP); err != nil {
			errs = append(errs, err)
		}
	}
	if len(desiredTCP) > 0 {
		if err := h.checkFirewalldInboundPorts(ctx, "tcp", desiredTCP); err != nil {
			errs = append(errs, err)
		}
	}
	h.inboundSynced, h.inboundKey, h.inboundErr, h.inboundAt = true, key, errors.Join(errs...), time.Now()
	return h.inboundErr
}

// ufwEnabled reads ENABLED= from ufw.conf, which `ufw enable` and `ufw disable` set. No file (UFW not installed) or
// "no" means UFW is off and is never run, so an inactive UFW costs a file read instead of a Python start.
func ufwEnabled(conf string) bool {
	if conf == "" {
		return false
	}
	b, err := os.ReadFile(conf)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ENABLED="); ok {
			return strings.EqualFold(strings.Trim(strings.TrimSpace(v), `"'`), "yes")
		}
	}
	return false
}

func normalizeUDPInboundPorts(ports []UDPInboundPort) ([]string, error) {
	set := make(map[string]struct{}, len(ports))
	for _, p := range ports {
		expr, err := p.expression()
		if err != nil {
			return nil, err
		}
		set[expr] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for expr := range set {
		out = append(out, expr)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeTCPInboundPorts(ports []uint16) ([]string, error) {
	set := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		if port == 0 {
			return nil, fmt.Errorf("invalid managed TCP port %d", port)
		}
		set[strconv.Itoa(int(port))] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for expr := range set {
		out = append(out, expr)
	}
	sort.Strings(out)
	return out, nil
}

func (p UDPInboundPort) expression() (string, error) {
	switch {
	case p.Port != 0 && p.From == 0 && p.To == 0:
		return strconv.Itoa(int(p.Port)), nil
	case p.Port == 0 && p.From != 0 && p.To >= p.From:
		if err := ValidateHop(Hop{InboundID: "inb_firewall", Network: "udp", From: p.From, To: p.To, Port: 443}, nil); err != nil {
			return "", fmt.Errorf("invalid managed UDP range %d-%d: %w", p.From, p.To, err)
		}
		return fmt.Sprintf("%d:%d", p.From, p.To), nil
	default:
		return "", fmt.Errorf("invalid managed UDP port %+v", p)
	}
}

// ufw runs ufw outside the agent's sandbox, as a transient systemd unit (the way awgprep runs its build): the agent's
// unit makes /etc/ufw read-only and has no CAP_NET_RAW, so ufw cannot change rules (and with iptables-legacy cannot
// even list them) from inside it. This keeps the agent's own sandbox as it is and works on every existing unit.
// --pipe returns ufw's output and --wait its exit status; RuntimeMaxSec stops a hung ufw before the timeout.
func (h *linuxHost) ufw(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, firewallCmdTimeout)
	defer cancel()
	run := append([]string{"--quiet", "--wait", "--pipe", "--collect", "--no-ask-password",
		fmt.Sprintf("--property=RuntimeMaxSec=%d", int((firewallCmdTimeout-5*time.Second)/time.Second)), "--", "ufw"}, args...)
	return h.run(ctx, "", "systemd-run", run...)
}

type ufwInboundRule struct {
	protocol string
	expr     string
}

func ufwInboundRuleKey(protocol, expr string) string { return protocol + "/" + expr }

func splitUFWInboundRuleKey(key string) (ufwInboundRule, bool) {
	protocol, expr, ok := strings.Cut(key, "/")
	if !ok || (protocol != "udp" && protocol != "tcp") || expr == "" {
		return ufwInboundRule{}, false
	}
	return ufwInboundRule{protocol: protocol, expr: expr}, true
}

func (h *linuxHost) syncUFWInboundPorts(ctx context.Context, desiredUDP, desiredTCP []string) error {
	out, err := h.ufw(ctx, "status")
	if err != nil {
		return fmt.Errorf("check UFW status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	active, err := ufwIsActive(out)
	if err != nil || !active {
		return err // Never change configured rules while UFW is inactive.
	}

	added, err := h.ufw(ctx, "show", "added")
	if err != nil {
		return fmt.Errorf("list UFW rules: %w: %s", err, strings.TrimSpace(string(added)))
	}
	owned, foreign, err := parseUFWRules(string(added))
	if err != nil {
		return err
	}
	desired := make([]ufwInboundRule, 0, len(desiredUDP)+len(desiredTCP))
	for _, expr := range desiredUDP {
		desired = append(desired, ufwInboundRule{protocol: "udp", expr: expr})
	}
	for _, expr := range desiredTCP {
		desired = append(desired, ufwInboundRule{protocol: "tcp", expr: expr})
	}
	sort.Slice(desired, func(i, j int) bool {
		if desired[i].protocol != desired[j].protocol {
			return desired[i].protocol < desired[j].protocol
		}
		return desired[i].expr < desired[j].expr
	})
	wanted := make(map[string]struct{}, len(desired))
	for _, rule := range desired {
		wanted[ufwInboundRuleKey(rule.protocol, rule.expr)] = struct{}{}
	}

	// Remove stale Mistgate-tagged rules before adding new ones. UFW accepts the same exact command
	// shape with a leading "delete", so untagged and pre-existing user rules are never selected.
	var stale []ufwInboundRule
	for key := range owned {
		if _, ok := wanted[key]; !ok {
			if rule, ok := splitUFWInboundRuleKey(key); ok {
				stale = append(stale, rule)
			}
		}
	}
	sort.Slice(stale, func(i, j int) bool {
		if stale[i].protocol != stale[j].protocol {
			return stale[i].protocol < stale[j].protocol
		}
		return stale[i].expr < stale[j].expr
	})
	for _, rule := range stale {
		if err := h.runUFWRule(ctx, "delete", rule.protocol, rule.expr); err != nil {
			return err
		}
	}
	var blocked []string
	for _, rule := range desired {
		key := ufwInboundRuleKey(rule.protocol, rule.expr)
		if _, ok := owned[key]; ok {
			continue
		}
		if action, ok := foreign[key]; ok {
			// The owner's (or the installer's) own rule for this port: never rewrite or later delete it.
			if action == "deny" || action == "reject" {
				blocked = append(blocked, fmt.Sprintf("%s %s/%s", action, rule.expr, rule.protocol))
			}
			continue
		}
		if err := h.runUFWRule(ctx, "allow", rule.protocol, rule.expr); err != nil {
			return err
		}
	}
	if len(blocked) > 0 {
		return fmt.Errorf("UFW has your own rule(s) %s for a Mistgate listener; Mistgate left them as they are", strings.Join(blocked, ", "))
	}
	return nil
}

func (h *linuxHost) runUFWRule(ctx context.Context, action, protocol, expr string) error {
	prefix := ufwInboundCommentPrefix
	if protocol == "tcp" {
		prefix = ufwInboundTCPCommentPrefix
	}
	comment := prefix + expr
	args := []string{"allow", expr + "/" + protocol, "comment", comment}
	if action == "delete" {
		args = append([]string{"delete"}, args...)
	}
	out, err := h.ufw(ctx, args...)
	if err != nil {
		return fmt.Errorf("ufw %s %s/%s: %w: %s", action, expr, protocol, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ufwIsActive(out []byte) (bool, error) {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	status, _, ok := strings.Cut(strings.TrimSpace(line), ":")
	if !ok || !strings.EqualFold(strings.TrimSpace(status), "Status") {
		return false, fmt.Errorf("unrecognized UFW status response %q", strings.TrimSpace(string(out)))
	}
	switch strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, status+":"))) {
	case "active":
		return true, nil
	case "inactive":
		return false, nil
	default:
		return false, fmt.Errorf("unrecognized UFW status response %q", strings.TrimSpace(string(out)))
	}
}

// parseUFWRules returns the protocol/port expressions of Mistgate's tagged rules and, for every other simple
// "<action> <port>/<protocol>" rule, its action.
func parseUFWRules(output string) (owned map[string]struct{}, foreign map[string]string, err error) {
	owned, foreign = map[string]struct{}{}, map[string]string{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		markedUDP := strings.Contains(line, ufwInboundCommentPrefix)
		markedTCP := strings.Contains(line, ufwInboundTCPCommentPrefix)
		if !markedUDP && !markedTCP {
			if m := ufwSimpleInboundRule.FindStringSubmatch(line); m != nil {
				foreign[ufwInboundRuleKey(m[3], m[2])] = m[1]
			}
			continue
		}
		comment, ok := ufwRuleComment(line)
		if !ok {
			return nil, nil, fmt.Errorf("cannot safely identify a Mistgate UFW rule in %q", line)
		}
		protocol, prefix := "udp", ufwInboundCommentPrefix
		if strings.HasPrefix(comment, ufwInboundTCPCommentPrefix) {
			protocol, prefix = "tcp", ufwInboundTCPCommentPrefix
		} else if !strings.HasPrefix(comment, ufwInboundCommentPrefix) {
			return nil, nil, fmt.Errorf("cannot safely identify a Mistgate UFW rule in %q", line)
		}
		expr := strings.TrimPrefix(comment, prefix)
		canonical, err := normalizeUFWPortExpression(expr, protocol)
		if err != nil || canonical != expr || !isUFWAllowInboundLine(line, expr, protocol) {
			return nil, nil, fmt.Errorf("cannot safely reconcile Mistgate UFW rule %q", line)
		}
		owned[ufwInboundRuleKey(protocol, expr)] = struct{}{}
	}
	return owned, foreign, nil
}

func ufwRuleComment(line string) (string, bool) {
	i := strings.LastIndex(line, " comment ")
	if i < 0 {
		return "", false
	}
	tail := strings.TrimSpace(line[i+len(" comment "):])
	if tail == "" {
		return "", false
	}
	if tail[0] == '\'' || tail[0] == '"' {
		quote := tail[0]
		end := strings.LastIndexByte(tail[1:], quote)
		if end < 0 || strings.TrimSpace(tail[end+2:]) != "" {
			return "", false
		}
		return tail[1 : end+1], true
	}
	if strings.ContainsAny(tail, " \t") {
		return "", false
	}
	return tail, true
}

func isUFWAllowInboundLine(line, expr, protocol string) bool {
	if !strings.HasPrefix(line, "ufw allow ") {
		return false
	}
	fields := strings.Fields(line)
	for i := 1; i < len(fields); i++ {
		if fields[i] == expr+"/"+protocol {
			return true
		}
		if fields[i] == "port" && i+2 < len(fields) && fields[i+1] == expr && fields[i+2] == "comment" {
			for j := 1; j+1 < len(fields); j++ {
				if fields[j] == "proto" && fields[j+1] == protocol {
					return true
				}
			}
		}
	}
	return false
}

func normalizeUFWPortExpression(expr, protocol string) (string, error) {
	network := strings.ToUpper(protocol)
	parts := strings.Split(expr, ":")
	if len(parts) == 1 {
		port, err := strconv.Atoi(parts[0])
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid %s port expression %q", network, expr)
		}
		return strconv.Itoa(port), nil
	}
	if len(parts) != 2 || protocol != "udp" {
		return "", fmt.Errorf("invalid %s range expression %q", network, expr)
	}
	from, errFrom := strconv.Atoi(parts[0])
	to, errTo := strconv.Atoi(parts[1])
	if errFrom != nil || errTo != nil || from < 1 || to > 65535 || to < from {
		return "", fmt.Errorf("invalid %s range expression %q", network, expr)
	}
	port := UDPInboundPort{From: uint16(from), To: uint16(to)}
	return port.expression()
}

// checkFirewalldInboundPorts asks a running firewalld whether its default zone opens each listener and reports only
// the ones it does not: Mistgate never edits firewalld (see SyncInboundPorts).
func (h *linuxHost) checkFirewalldInboundPorts(ctx context.Context, protocol string, desired []string) error {
	ctx, cancel := context.WithTimeout(ctx, firewallCmdTimeout)
	defer cancel()
	out, err := h.run(ctx, "", "firewall-cmd", "--state")
	if err != nil {
		if commandMissing(err) || strings.Contains(strings.ToLower(string(out)), "not running") {
			return nil
		}
		return fmt.Errorf("check firewalld status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != "running" {
		return fmt.Errorf("unrecognized firewalld status response %q", strings.TrimSpace(string(out)))
	}
	var missing []string
	for _, expr := range desired {
		port := strings.ReplaceAll(expr, ":", "-") + "/" + protocol
		// "yes" (exit 0) or "no" (exit 1); anything else is an error worth showing.
		out, err := h.run(ctx, "", "firewall-cmd", "--query-port="+port)
		switch strings.TrimSpace(string(out)) {
		case "yes":
		case "no":
			missing = append(missing, port)
		default:
			return fmt.Errorf("query firewalld port %s: %v: %s", port, err, strings.TrimSpace(string(out)))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("firewalld is active and its default zone does not open %s; Mistgate does not edit firewalld rules, add these %s ports to the active zone(s) by hand", strings.Join(missing, ", "), strings.ToUpper(protocol))
}

func commandMissing(err error) bool {
	var ee *exec.Error
	return errors.As(err, &ee) && errors.Is(ee.Err, exec.ErrNotFound)
}
