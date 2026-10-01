package doctor

import (
	"context"
	"fmt"
	"strconv"
)

// disk_space: logs or disk over 80%; journals of 1-4 GB and a 1.1 GB btmp have filled disks.
func checkDiskSpace(ctx context.Context, e *Env) Result {
	mounts := []string{"/"}
	if e.StateDir != "" {
		mounts = append(mounts, e.StateDir)
	}
	type row struct {
		mount                     string
		st                        Status
		usedPct, inodePct, freeMB int
	}
	var rows []row
	var prev []FSStat
	for _, m := range mounts {
		s, err := e.Statfs(m)
		if err != nil || s.Blocks == 0 {
			continue
		}
		if dup(prev, s) { // the state dir is on the same volume
			continue
		}
		prev = append(prev, s)
		used := s.Blocks - s.Bfree
		usedPct := pct(used, used+s.Bavail) // the way df counts: root's reserved blocks are not free space
		free := s.Bavail * s.BlockSize
		inodePct := 0
		if s.Files > 0 {
			inodePct = pct(s.Files-s.Ffree, s.Files)
		}
		st := OK
		switch {
		case usedPct >= diskFailUsedPct || free < diskFailFree || inodePct >= diskFailInodePct:
			st = Fail
		case usedPct >= diskWarnUsedPct || free < diskWarnFree:
			st = Warn
		}
		rows = append(rows, row{m, st, usedPct, inodePct, int(free >> 20)})
	}
	if len(rows) == 0 {
		return skip(CodeDiskUnreadable, "statfs failed for every mount")
	}
	w := rows[0]
	for _, r := range rows[1:] {
		if r.st > w.st || r.st == w.st && r.usedPct > w.usedPct {
			w = r
		}
	}
	j := journal(e)
	params := p("mount", w.mount, "used_pct", strconv.Itoa(w.usedPct), "free_mb", strconv.Itoa(w.freeMB),
		"inode_pct", strconv.Itoa(w.inodePct), "journal_mb", strconv.Itoa(int(j.bytes>>20)))
	if fi, err := e.stat("/var/log/btmp"); err == nil && fi.Size() >= 1<<20 {
		params["btmp_mb"] = strconv.Itoa(int(fi.Size() >> 20)) // named only: truncating it is not in the safe set
	}
	r := Result{Status: w.st, Params: params, Code: CodeDiskUsage,
		Detail: fmt.Sprintf("%s %d%% used, %s free, inodes %d%%", w.mount, w.usedPct, human(uint64(w.freeMB)<<20), w.inodePct)}
	if w.st != OK && j.bytes >= diskVacuumMin && e.has("journalctl") {
		r.FixID = FixJournaldVacuum
	}
	return r
}

func dup(seen []FSStat, s FSStat) bool {
	for _, o := range seen {
		if o == s {
			return true
		}
	}
	return false
}

func pct(part, whole uint64) int {
	if whole == 0 {
		return 0
	}
	return int(part * 100 / whole)
}

// human prints bytes as "1.2 GB" / "300 MB".
func human(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%d MB", b>>20)
	}
	return fmt.Sprintf("%d KB", b>>10)
}

// journald_size: the baseline caps the journal at 200 MB; over 300 MB the drop-in is missing or not effective.
func checkJournaldSize(ctx context.Context, e *Env) Result {
	j := journal(e)
	if !j.found {
		return skip(CodeJournalNone, "no systemd journal directory")
	}
	mb := strconv.Itoa(int(j.bytes >> 20))
	r := Result{Status: OK, Params: p("journal_mb", mb, "cap_mb", strconv.Itoa(journalCap>>20)), Code: CodeJournalSize,
		Detail: "journal " + human(uint64(j.bytes)) + " on disk"}
	switch {
	case j.bytes > journalFail:
		r.Status = Fail
	case j.bytes > journalWarn:
		r.Status = Warn
	}
	if r.Status != OK && e.has("journalctl") {
		r.FixID = FixJournaldVacuum
	}
	return r
}
