//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const ufwInboundCommentPrefix = "mistgate-node-managed-udp-v1-"

// SyncInboundUDPPorts manages only UFW rules carrying Mistgate's private comment tag. It leaves an
// inactive firewall untouched. firewalld is detected but deliberately not edited: its CLI has no
// runtime-only, per-rule ownership marker, and activating a custom service requires a global reload
// that would discard unrelated runtime-only changes.
func (h *linuxHost) SyncInboundUDPPorts(ctx context.Context, ports []UDPInboundPort) error {
	desired, err := normalizeUDPInboundPorts(ports)
	if err != nil {
		return err
	}
	h.fw.Lock()
	defer h.fw.Unlock()

	var errs []error
	if err := h.syncUFWInboundUDPPorts(ctx, desired); err != nil {
		errs = append(errs, err)
	}
	if len(desired) > 0 {
		if err := h.checkFirewalldInboundUDPPorts(ctx, desired); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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

func (h *linuxHost) syncUFWInboundUDPPorts(ctx context.Context, desired []string) error {
	out, err := h.run(ctx, "", "ufw", "status")
	if err != nil {
		if commandMissing(err) {
			return nil // UFW is not installed; do not install or enable a firewall.
		}
		return fmt.Errorf("check UFW status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	active, err := ufwIsActive(out)
	if err != nil || !active {
		return err // Never change configured rules while UFW is inactive.
	}

	added, err := h.run(ctx, "", "ufw", "show", "added")
	if err != nil {
		return fmt.Errorf("list UFW rules: %w: %s", err, strings.TrimSpace(string(added)))
	}
	owned, err := parseManagedUFWRules(string(added))
	if err != nil {
		return err
	}
	wanted := make(map[string]struct{}, len(desired))
	for _, expr := range desired {
		wanted[expr] = struct{}{}
	}

	// Remove stale Mistgate-tagged rules before adding new ones. UFW accepts the same exact command
	// shape with a leading "delete", so untagged and pre-existing user rules are never selected.
	var stale []string
	for expr := range owned {
		if _, ok := wanted[expr]; !ok {
			stale = append(stale, expr)
		}
	}
	sort.Strings(stale)
	for _, expr := range stale {
		if err := h.runUFWRule(ctx, "delete", expr); err != nil {
			return err
		}
	}
	for _, expr := range desired {
		if _, ok := owned[expr]; ok {
			continue
		}
		if err := h.runUFWRule(ctx, "allow", expr); err != nil {
			return err
		}
	}
	return nil
}

func (h *linuxHost) runUFWRule(ctx context.Context, action, expr string) error {
	comment := ufwInboundCommentPrefix + expr
	args := []string{"allow", expr + "/udp", "comment", comment}
	if action == "delete" {
		args = append([]string{"delete"}, args...)
	}
	out, err := h.run(ctx, "", "ufw", args...)
	if err != nil {
		return fmt.Errorf("ufw %s %s/udp: %w: %s", action, expr, err, strings.TrimSpace(string(out)))
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

func parseManagedUFWRules(output string) (map[string]struct{}, error) {
	owned := map[string]struct{}{}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.Contains(line, ufwInboundCommentPrefix) {
			continue
		}
		comment, ok := ufwRuleComment(line)
		if !ok || !strings.HasPrefix(comment, ufwInboundCommentPrefix) {
			return nil, fmt.Errorf("cannot safely identify a Mistgate UFW rule in %q", line)
		}
		expr := strings.TrimPrefix(comment, ufwInboundCommentPrefix)
		canonical, err := normalizeUFWPortExpression(expr)
		if err != nil || canonical != expr || !isUFWAllowUDPLine(line, expr) {
			return nil, fmt.Errorf("cannot safely reconcile Mistgate UFW rule %q", line)
		}
		owned[expr] = struct{}{}
	}
	return owned, nil
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

func isUFWAllowUDPLine(line, expr string) bool {
	if !strings.HasPrefix(line, "ufw allow ") {
		return false
	}
	fields := strings.Fields(line)
	for i := 1; i < len(fields); i++ {
		if fields[i] == expr+"/udp" {
			return true
		}
		if fields[i] == "port" && i+2 < len(fields) && fields[i+1] == expr && fields[i+2] == "comment" {
			for j := 1; j+1 < len(fields); j++ {
				if fields[j] == "proto" && fields[j+1] == "udp" {
					return true
				}
			}
		}
	}
	return false
}

func normalizeUFWPortExpression(expr string) (string, error) {
	parts := strings.Split(expr, ":")
	if len(parts) == 1 {
		port, err := strconv.Atoi(parts[0])
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid UDP port expression %q", expr)
		}
		return strconv.Itoa(port), nil
	}
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid UDP range expression %q", expr)
	}
	from, errFrom := strconv.Atoi(parts[0])
	to, errTo := strconv.Atoi(parts[1])
	if errFrom != nil || errTo != nil || from < 1 || to > 65535 || to < from {
		return "", fmt.Errorf("invalid UDP range expression %q", expr)
	}
	port := UDPInboundPort{From: uint16(from), To: uint16(to)}
	return port.expression()
}

func (h *linuxHost) checkFirewalldInboundUDPPorts(ctx context.Context, desired []string) error {
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
	ports := make([]string, len(desired))
	for i, expr := range desired {
		ports[i] = strings.ReplaceAll(expr, ":", "-") + "/udp"
	}
	return fmt.Errorf("firewalld is active, but Mistgate cannot safely own individual firewalld port rules without reloading shared runtime configuration; add these exact UDP listeners to the active firewalld zone(s) manually: %s", strings.Join(ports, ", "))
}

func commandMissing(err error) bool {
	var ee *exec.Error
	return errors.As(err, &ee) && errors.Is(ee.Err, exec.ErrNotFound)
}
