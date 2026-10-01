//go:build linux

package hostctl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Safe fixes for the doctor. Nothing here is reached without an explicit ApplyFix from the panel.

var _ Fixer = (*linuxHost)(nil)

const (
	defaultResolvedFile   = "/etc/systemd/resolved.conf.d/90-mistgate.conf"
	defaultResolvConfFile = "/etc/resolv.conf"
	backupSuffix          = ".mistgate.bak"      // original content of resolv.conf
	backupLinkSuffix      = ".mistgate.bak.link" // its symlink target, when it was a symlink
)

// VacuumJournal trims the systemd journal to the cap baseline installs (SystemMaxUse) and to a week.
func (h *linuxHost) VacuumJournal(ctx context.Context) error {
	out, err := h.run(ctx, "", "journalctl", fmt.Sprintf("--vacuum-size=%dM", JournalCapMB), "--vacuum-time=7d")
	if err != nil {
		return fmt.Errorf("journalctl vacuum: %w: %s", err, tail(out))
	}
	return nil
}

func tail(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) > 200 {
		b = b[len(b)-200:]
	}
	return string(b)
}

// resolverMode is "resolved" when systemd-resolved is running (a resolv.conf edit would be overwritten or
// ignored), else "resolv_conf".
func (h *linuxHost) resolverMode(ctx context.Context) string {
	if _, err := h.run(ctx, "", "systemctl", "is-active", "--quiet", "systemd-resolved"); err == nil {
		return ResolverModeResolved
	}
	return ResolverModeResolvConf
}

func (h *linuxHost) resolverPaths() (resolved, conf string) {
	return h.resolvedFile, h.resolvConfFile
}

func (h *linuxHost) currentResolvers(ctx context.Context, mode string) []string {
	if mode == ResolverModeResolved {
		if out, err := h.run(ctx, "", "resolvectl", "dns"); err == nil {
			if r := parseResolvectlDNS(string(out)); len(r) > 0 {
				return r
			}
		}
	}
	_, conf := h.resolverPaths()
	b, _ := os.ReadFile(conf)
	return parseNameservers(string(b))
}

func (h *linuxHost) ResolverPlan(ctx context.Context, servers []string) (ResolverPlan, error) {
	resolved, conf := h.resolverPaths()
	if resolved == "" || conf == "" {
		return ResolverPlan{}, ErrUnsupported
	}
	mode := h.resolverMode(ctx)
	after, err := cleanResolvers(servers, mode == ResolverModeResolvConf)
	if err != nil {
		return ResolverPlan{}, err
	}
	return ResolverPlan{Mode: mode, Before: h.currentResolvers(ctx, mode), After: after}, nil
}

func (h *linuxHost) SetResolver(ctx context.Context, servers []string) error {
	plan, err := h.ResolverPlan(ctx, servers)
	if err != nil {
		return err
	}
	resolved, conf := h.resolverPaths()
	if plan.Mode == ResolverModeResolved {
		if _, err := writeIfChanged(resolved, renderResolvedDropIn(plan.After), 0o644); err != nil {
			return err
		}
		if out, err := h.run(ctx, "", "systemctl", "restart", "systemd-resolved"); err != nil {
			return fmt.Errorf("restart systemd-resolved: %w: %s", err, tail(out))
		}
		return nil
	}
	if err := backupResolvConf(conf); err != nil {
		return err
	}
	_, err = writeIfChanged(conf, renderResolvConf(plan.After), 0o644)
	return err
}

// backupResolvConf keeps the original resolv.conf once. A symlink (stub resolver, resolvconf) is remembered
// by target so Cleanup can put the link back instead of a frozen copy of what it pointed at.
func backupResolvConf(conf string) error {
	bak := conf + backupSuffix
	if _, err := os.Lstat(bak); err == nil {
		return nil // already backed up by an earlier fix; never overwrite the original with our own file
	}
	if cur, err := os.ReadFile(conf); err == nil && strings.HasPrefix(string(cur), resolverMarker) {
		return nil // ours without a backup (lost): nothing better to keep
	}
	if target, err := os.Readlink(conf); err == nil {
		if err := os.WriteFile(conf+backupLinkSuffix, []byte(target), 0o644); err != nil {
			return err
		}
	}
	orig, _ := os.ReadFile(conf) // a dangling link or a missing file backs up as empty
	if err := os.WriteFile(bak, orig, 0o644); err != nil {
		return err
	}
	return nil
}

// undoResolver removes what SetResolver installed. Called by Cleanup; it works from the files on disk so it
// also undoes a fix made before an agent restart. resolv.conf is restored only while it is still ours.
func (h *linuxHost) undoResolver(ctx context.Context) error {
	resolved, conf := h.resolverPaths()
	if resolved == "" || conf == "" {
		return nil
	}
	var errs []error
	if err := os.Remove(resolved); err == nil {
		if out, err := h.run(ctx, "", "systemctl", "restart", "systemd-resolved"); err != nil {
			errs = append(errs, fmt.Errorf("restart systemd-resolved: %w: %s", err, tail(out)))
		}
	} else if !os.IsNotExist(err) {
		errs = append(errs, err)
	}
	bak, link := conf+backupSuffix, conf+backupLinkSuffix
	if _, err := os.Lstat(bak); err == nil {
		if cur, err := os.ReadFile(conf); err == nil && strings.HasPrefix(string(cur), resolverMarker) {
			if target, err := os.ReadFile(link); err == nil {
				errs = append(errs, restoreLink(conf, string(target)))
			} else {
				orig, _ := os.ReadFile(bak)
				_, err := writeIfChanged(conf, string(orig), 0o644)
				errs = append(errs, err)
			}
		}
		_ = os.Remove(bak)
		_ = os.Remove(link)
	}
	return errors.Join(errs...)
}

func restoreLink(conf, target string) error {
	tmp := conf + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, conf); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
