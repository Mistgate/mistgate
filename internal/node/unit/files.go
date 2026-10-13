package unit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const RefreshOutcomeFile = "unit-refresh.outcome"

type RefreshOutcome struct {
	At      time.Time `json:"at"`
	FromGen int       `json:"from_gen"`
	ToGen   int       `json:"to_gen"`
	Result  string    `json:"result"`
	Reason  string    `json:"reason,omitempty"`
}

const (
	RefreshOK         = "ok"
	RefreshRolledBack = "rolled_back"
	RefreshFailed     = "failed"
)

func WriteRefreshOutcome(path string, outcome RefreshOutcome) error {
	if outcome.At.IsZero() || outcome.FromGen < 0 || outcome.ToGen <= outcome.FromGen || !validRefreshResult(outcome.Result) {
		return errors.New("invalid unit refresh outcome")
	}
	b, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	return atomicWrite(path, b, 0o600)
}

func ReadRefreshOutcome(path string) (RefreshOutcome, error) {
	f, err := os.Open(path)
	if err != nil {
		return RefreshOutcome{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return RefreshOutcome{}, err
	}
	if len(b) > 4096 {
		return RefreshOutcome{}, errors.New("unit refresh outcome is too large")
	}
	var outcome RefreshOutcome
	if err := json.Unmarshal(b, &outcome); err != nil {
		return RefreshOutcome{}, err
	}
	if outcome.At.IsZero() || outcome.FromGen < 0 || outcome.ToGen <= outcome.FromGen || !validRefreshResult(outcome.Result) {
		return RefreshOutcome{}, errors.New("invalid unit refresh outcome")
	}
	return outcome, nil
}

func RemoveRefreshOutcome(path string) error { return removeIfExists(path) }

func WriteRefreshAck(path, token string) error {
	if len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
		return errors.New("invalid unit refresh ack token")
	}
	return atomicWrite(path, []byte(token), 0o600)
}

func WaitRefreshAck(ctx context.Context, path, token string) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if b, err := os.ReadFile(path); err == nil && string(b) == token {
			_ = removeIfExists(path)
			return nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read refresh ack: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func WriteUnitWithPrevious(path string, contents []byte) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("read current unit: %w", err)
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("current unit is not a regular file")
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read current unit: %w", err)
	}
	dir := filepath.Dir(path)
	mode := info.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	tmp, err := writeTemp(dir, filepath.Base(path)+".new-", contents, mode)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp)
	if err := atomicWrite(path+".prev", old, mode); err != nil {
		return false, fmt.Errorf("save previous unit: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, fmt.Errorf("replace unit: %w", err)
	}
	if err := syncDir(dir); err != nil {
		_ = atomicWrite(path, old, mode)
		return false, fmt.Errorf("sync unit directory: %w", err)
	}
	return true, nil
}

func RestorePreviousUnit(path string) error {
	prev := path + ".prev"
	info, err := os.Stat(prev)
	if err != nil {
		return fmt.Errorf("read previous unit: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("previous unit is not a regular file")
	}
	b, err := os.ReadFile(prev)
	if err != nil {
		return fmt.Errorf("read previous unit: %w", err)
	}
	if err := atomicWrite(path, b, info.Mode().Perm()); err != nil {
		return fmt.Errorf("restore previous unit: %w", err)
	}
	return nil
}

func atomicWrite(path string, contents []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := writeTemp(dir, filepath.Base(path)+".tmp-", contents, mode)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func writeTemp(dir, pattern string, contents []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	cleanup := func() { _ = f.Close(); _ = os.Remove(name) }
	if err := f.Chmod(mode); err != nil {
		cleanup()
		return "", err
	}
	if _, err := f.Write(contents); err != nil {
		cleanup()
		return "", err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validRefreshResult(result string) bool {
	return result == RefreshOK || result == RefreshRolledBack || result == RefreshFailed
}
