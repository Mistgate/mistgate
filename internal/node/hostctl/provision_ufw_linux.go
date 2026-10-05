//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// provisionUFWRules are the rules the SSH install may have added with ProvisionUFWTag.
var provisionUFWRules = []string{"80/tcp", "443/tcp", "443/udp"}

// removeProvisionUFWRules deletes from an active UFW exactly the rules listed by `ufw show added` as
// "ufw allow <rule> comment '<ProvisionUFWTag>'". Untagged rules, the owner's own, are never selected. Like the UDP
// sync it runs ufw outside the agent's sandbox (h.ufw), and not at all while UFW is off.
func (h *linuxHost) removeProvisionUFWRules(ctx context.Context) error {
	if !ufwEnabled(h.ufwConf) {
		return nil
	}
	h.fw.Lock()
	defer h.fw.Unlock()
	out, err := h.ufw(ctx, "status")
	if err != nil {
		return fmt.Errorf("check UFW status: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if active, err := ufwIsActive(out); err != nil || !active {
		return err
	}
	added, err := h.ufw(ctx, "show", "added")
	if err != nil {
		return fmt.Errorf("list UFW rules: %w: %s", err, strings.TrimSpace(string(added)))
	}
	lines := map[string]bool{}
	for _, line := range strings.Split(string(added), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	var errs []error
	for _, rule := range provisionUFWRules {
		if !lines["ufw allow "+rule+" comment '"+ProvisionUFWTag+"'"] {
			continue
		}
		if out, err := h.ufw(ctx, "delete", "allow", rule, "comment", ProvisionUFWTag); err != nil {
			errs = append(errs, fmt.Errorf("ufw delete allow %s: %w: %s", rule, err, strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(errs...)
}
