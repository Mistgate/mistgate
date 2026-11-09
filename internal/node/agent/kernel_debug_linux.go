//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultJournalctlPath = "journalctl"
	defaultMessageCost    = "/proc/sys/net/core/message_cost"
)

func kernelDebugSupported() bool { return true }

func newKernelDebugEnvironment(opts kernelDebugOptions) *kernelDebugEnvironment {
	if opts.journalctlPath == "" {
		opts.journalctlPath = defaultJournalctlPath
	}
	if len(opts.dynamicDebugPaths) == 0 {
		opts.dynamicDebugPaths = []string{"/proc/dynamic_debug/control", "/sys/kernel/debug/dynamic_debug/control"}
	}
	if opts.messageCostPath == "" {
		opts.messageCostPath = defaultMessageCost
	}
	if opts.startJournal == nil {
		opts.startJournal = startKernelJournalCommand
	}
	return &kernelDebugEnvironment{opts: opts}
}

func (k *kernelDebugEnvironment) resetAtStart() []error {
	var errs []error
	if err := k.writeDynamicDebug("module amneziawg -p"); err != nil {
		errs = append(errs, fmt.Errorf("disable AmneziaWG dynamic debug: %w", err))
	}
	value, err := readMessageCost(k.opts.messageCostPath)
	if err != nil {
		errs = append(errs, fmt.Errorf("read net.core.message_cost: %w", err))
	} else if value == 0 {
		if err := writeMessageCost(k.opts.messageCostPath, 5); err != nil {
			errs = append(errs, fmt.Errorf("restore net.core.message_cost: %w", err))
		}
	}
	return errs
}

func (k *kernelDebugEnvironment) enable() *kernelDebugLease {
	var errs []error
	oldCost, changedCost := 0, false
	value, err := readMessageCost(k.opts.messageCostPath)
	if err != nil {
		errs = append(errs, fmt.Errorf("read net.core.message_cost: %w", err))
	} else {
		oldCost = value
		if value == 0 {
			if err := writeMessageCost(k.opts.messageCostPath, 5); err != nil {
				errs = append(errs, fmt.Errorf("set net.core.message_cost: %w", err))
			} else {
				oldCost = 5
			}
		}
		if err := writeMessageCost(k.opts.messageCostPath, 0); err != nil {
			errs = append(errs, fmt.Errorf("disable net.core.message_cost ratelimit: %w", err))
		} else {
			changedCost = true
		}
	}
	debugErr := k.writeDynamicDebug("module amneziawg +p")
	if debugErr != nil {
		errs = append(errs, fmt.Errorf("enable AmneziaWG dynamic debug: %w", debugErr))
	}
	return &kernelDebugLease{debugOn: debugErr == nil, begin: errs, closeFn: func() []error {
		var endErrs []error
		if err := k.writeDynamicDebug("module amneziawg -p"); err != nil {
			endErrs = append(endErrs, fmt.Errorf("disable AmneziaWG dynamic debug: %w", err))
		}
		if changedCost {
			if err := writeMessageCost(k.opts.messageCostPath, oldCost); err != nil {
				endErrs = append(endErrs, fmt.Errorf("restore net.core.message_cost: %w", err))
			}
		}
		return endErrs
	}}
}

func (k *kernelDebugEnvironment) startJournal(ctx context.Context, since time.Time) (kernelJournalProcess, error) {
	return k.opts.startJournal(ctx, k.opts.journalctlPath, "--dmesg", "--follow", "--output=json", "--no-pager", "--quiet", "--since=@"+strconv.FormatInt(since.Unix(), 10))
}

func (k *kernelDebugEnvironment) writeDynamicDebug(command string) error {
	var errs []error
	for _, path := range k.opts.dynamicDebugPaths {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		_, writeErr := io.WriteString(file, command+"\n")
		closeErr := file.Close()
		if writeErr == nil && closeErr == nil {
			return nil
		}
		if writeErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, writeErr))
		}
		if closeErr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, closeErr))
		}
	}
	return errors.Join(errs...)
}

func readMessageCost(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid value in %s", path)
	}
	return value, nil
}

func writeMessageCost(path string, value int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(value)+"\n"), 0o600)
}

type execKernelJournalProcess struct {
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	waitMu  sync.Mutex
	waited  bool
	waitErr error
}

func (p *execKernelJournalProcess) Read(b []byte) (int, error) { return p.stdout.Read(b) }
func (p *execKernelJournalProcess) Close() error               { return p.stdout.Close() }
func (p *execKernelJournalProcess) Wait() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	if !p.waited {
		p.waitErr = p.cmd.Wait()
		p.waited = true
	}
	return p.waitErr
}

func startKernelJournalCommand(ctx context.Context, name string, args ...string) (kernelJournalProcess, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return nil, err
	}
	return &execKernelJournalProcess{cmd: cmd, stdout: stdout}, nil
}
