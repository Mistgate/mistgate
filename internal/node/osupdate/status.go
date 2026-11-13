package osupdate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// StatusFileName is the job's state file in the agent's state directory (mode 0600). The job writes it with a heartbeat;
// the agent reads it, also after its own restart and after a reboot of the host.
const StatusFileName = "os-update.json"

const (
	// Stale is how long a running job may go without a heartbeat before the agent calls it dead.
	Stale = 2 * time.Minute
	// HeartbeatEvery is the job's heartbeat.
	HeartbeatEvery = 15 * time.Second
)

// Status is the state file. The job is its only writer until it ends; after that the agent writes it, once, to set Reported
// (and, for a job that rebooted the host, to record how the node came back).
type Status struct {
	JobID  string `json:"job_id"`
	Action string `json:"action"`
	Mode   string `json:"mode,omitempty"`
	// RebootPolicy is the reboot value of the request (never | if_required | always).
	RebootPolicy string `json:"reboot_policy"`
	State        string `json:"state"`
	Phase        string `json:"phase,omitempty"`
	Started      int64  `json:"started"` // Unix seconds, the host clock
	Updated      int64  `json:"updated"` // the last heartbeat
	Finished     int64  `json:"finished,omitempty"`
	// Back is when the agent first saw the host on a new boot (a job that rebooted it).
	Back          int64    `json:"back,omitempty"`
	Code          string   `json:"code,omitempty"`
	Reason        string   `json:"reason,omitempty"` // one fixed English sentence, never package-manager output
	Package       string   `json:"package,omitempty"`
	Reasons       []string `json:"reasons,omitempty"` // every code that blocked the reboot
	Upgraded      uint32   `json:"upgraded,omitempty"`
	Reboot        string   `json:"reboot,omitempty"`
	HeldReason    string   `json:"held_reason,omitempty"`
	SourcesFailed []string `json:"sources_failed,omitempty"`
	KernelFrom    string   `json:"kernel_from,omitempty"`
	KernelTo      string   `json:"kernel_to,omitempty"`
	KernelNow     string   `json:"kernel_now,omitempty"`
	BootID        string   `json:"boot_id,omitempty"` // the boot the job started on
	AwgRequired   bool     `json:"awg_required,omitempty"`
	// AwgSource says what the job found in the module's source: "patched" (it repaired a known-bad compat.h) or
	// "unknown:<12 hex of its sha256>" (a file Mistgate does not know, left as it was). Empty = nothing to say.
	AwgSource string `json:"awg_source,omitempty"`
	Reported  bool   `json:"reported,omitempty"`
}

// ReadStatus returns the status at path; ok is false when there is none (never ran, or unreadable garbage).
func ReadStatus(path string) (s Status, ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	if json.Unmarshal(b, &s) != nil || (s.State != StateRunning && s.State != StateDone && s.State != StateFailed) {
		return Status{}, false, nil // a half-written or foreign file is "nothing", never a reason to block a new run
	}
	return s, true, nil
}

// WriteStatus replaces the file atomically and flushes it: the file with phase "rebooting" has to be on disk when the host
// goes down.
func WriteStatus(path string, s Status) error { return writeJSONFile(path, s) }

// writeJSONFile writes v as JSON to path (mode 0600) through a temporary file, flushes it and the directory, and renames it
// into place.
func writeJSONFile(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Proto is the OsStatus.job form of the status.
func (s Status) Proto() *agentv1.OsJob {
	return &agentv1.OsJob{
		JobId: s.JobID, Action: s.Action, Mode: s.Mode, State: s.State, Phase: s.Phase,
		StartedUnix: s.Started, UpdatedUnix: s.Updated, FinishedUnix: s.Finished,
		Code: s.Code, Reason: s.Reason, Upgraded: s.Upgraded, Reboot: s.Reboot, HeldReason: s.HeldReason,
		SourcesFailed: append([]string(nil), s.SourcesFailed...), KernelFrom: s.KernelFrom, KernelTo: s.KernelTo,
	}
}
