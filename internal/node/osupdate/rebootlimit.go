package osupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The reboot history: a node reboots for the panel's jobs at most once an hour and three times a day, however many commands
// arrive and whatever the panel (or whoever holds it) asks. It is a file next to the job's status file, so it survives a restart
// of the agent and the reboot itself. Jobs started by hand (manual- ids of the command line) are not limited: the owner is at
// the keyboard. A history that cannot be trusted (unreadable, or ahead of the clock) stops a panel job's reboot.
const (
	RebootsFileName = "os-reboots.json"
	MaxRebootsHour  = 1
	MaxRebootsDay   = 3
	// clockSkew is how far ahead of the clock the newest record may be before the clock is called wrong.
	clockSkew = time.Minute
)

type rebootLog struct {
	Reboots []int64 `json:"reboots"` // Unix seconds, the host clock, when a reboot command was given
}

func (r *run) limited() bool { return strings.HasPrefix(r.j.Spec.JobID, "osj_") }

func (j Job) rebootLogPath() string {
	switch {
	case j.RebootLogPath != "":
		return j.RebootLogPath
	case j.StatusPath != "":
		return filepath.Join(filepath.Dir(j.StatusPath), RebootsFileName)
	}
	return ""
}

func (r *run) loadRebootLog() (rebootLog, error) {
	path := r.j.rebootLogPath()
	if path == "" {
		return rebootLog{}, errors.New("there is no place to keep the reboot history")
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return rebootLog{}, nil
	}
	if err != nil {
		return rebootLog{}, err
	}
	var l rebootLog
	if err := json.Unmarshal(b, &l); err != nil {
		return rebootLog{}, errors.New("the reboot history file is damaged")
	}
	return l, nil
}

// rebootLimitProblem says why a panel job may not reboot now, or "" when it may.
func (r *run) rebootLimitProblem() string {
	if !r.limited() {
		return ""
	}
	l, err := r.loadRebootLog()
	if err != nil {
		return "the reboot history cannot be trusted (" + err.Error() + "), so the reboot is not done"
	}
	now := r.host.Now()
	var hour, day int
	for _, t := range l.Reboots {
		at := time.Unix(t, 0)
		if at.After(now.Add(clockSkew)) {
			return "the last recorded reboot is ahead of the clock of this host, so the reboot is not done"
		}
		if now.Sub(at) < time.Hour {
			hour++
		}
		if now.Sub(at) < 24*time.Hour {
			day++
		}
	}
	switch {
	case hour >= MaxRebootsHour:
		return fmt.Sprintf("this node was rebooted less than an hour ago; a panel job reboots it %d time per hour at most", MaxRebootsHour)
	case day >= MaxRebootsDay:
		return fmt.Sprintf("this node was rebooted %d times in the last 24 hours; a panel job reboots it %d times a day at most", day, MaxRebootsDay)
	}
	return ""
}

// recordReboot adds this reboot to the history (and forgets what is older than a day). Jobs of the command line are not counted.
func (r *run) recordReboot() error {
	if !r.limited() {
		return nil
	}
	l, err := r.loadRebootLog()
	if err != nil {
		return err
	}
	now := r.host.Now()
	kept := []int64{now.Unix()}
	for _, t := range l.Reboots {
		if now.Sub(time.Unix(t, 0)) < 24*time.Hour {
			kept = append(kept, t)
		}
	}
	return writeJSONFile(r.j.rebootLogPath(), rebootLog{Reboots: kept})
}
