package osupdate

import (
	"context"
	"sort"
	"strings"
)

// grubProblem says why the next boot may not start "the newest kernel", or "" when it will; see grubDefaultProblem.
func (r *run) grubProblem(ctx context.Context) string {
	return grubDefaultProblem(r.host.exists, r.host.Glob, r.host.text, func() (string, error) { return r.query(ctx, "grub-editenv", "list") })
}

// grubDefaultProblem is the check behind grubProblem and the reader's reboot preview, over the files and the one command they read.
// The job checks the newest kernel and the ones below it down to the running one; that is only worth anything when GRUB starts the
// first menu entry. It refuses when:
//   - there is no /boot/grub/grub.cfg (another boot loader, or a GRUB whose configuration is somewhere else);
//   - GRUB_DEFAULT is set in /etc/default/grub or /etc/default/grub.d/*.cfg to anything but 0 ("saved", an entry number or name);
//   - grub-editenv shows a saved_entry or a next_entry (a one-shot or remembered choice), or cannot be read.
func grubDefaultProblem(exists func(string) bool, glob func(string) []string, text func(string) string, editenv func() (string, error)) string {
	if !exists("/boot/grub/grub.cfg") {
		return "there is no /boot/grub/grub.cfg: the boot loader is not GRUB or its configuration is elsewhere, so the kernel of the next boot is not known"
	}
	files := []string{"/etc/default/grub"}
	extra := glob("/etc/default/grub.d/*.cfg")
	sort.Strings(extra)
	files = append(files, extra...)
	def := ""
	for _, f := range files {
		if v, ok := grubDefaultIn(text(f)); ok {
			def = v // grub-mkconfig sources the files in this order; the last assignment wins
		}
	}
	if def != "" && def != "0" {
		return "GRUB_DEFAULT is set to something other than 0, so GRUB may not start the newest kernel"
	}
	if !exists("/boot/grub/grubenv") {
		return "" // no environment block: nothing is saved, and grub-editenv would only fail to open it
	}
	out, err := editenv()
	if err != nil {
		return "grub-editenv could not be read, so a saved or next boot entry cannot be ruled out"
	}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && (k == "saved_entry" || k == "next_entry") && v != "" {
			return "GRUB has a " + k + " set, so it may not start the newest kernel"
		}
	}
	return ""
}

// grubDefaultIn finds the last GRUB_DEFAULT assignment of a shell fragment: its value without quotes or a trailing comment.
func grubDefaultIn(body string) (string, bool) {
	val, found := "", false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		rest, ok := strings.CutPrefix(line, "GRUB_DEFAULT=")
		if !ok {
			continue
		}
		val, found = shellWord(rest), true
	}
	return val, found
}

// shellWord is the first word of an assignment's right side: a quoted string, or text up to a blank or a comment.
func shellWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if q := s[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(s[1:], q); end >= 0 {
			return s[1 : 1+end]
		}
		return s[1:]
	}
	if i := strings.IndexAny(s, " \t#;"); i >= 0 {
		s = s[:i]
	}
	return s
}
