//go:build !linux

package agent

import (
	"context"
	"errors"
	"time"
)

func kernelDebugSupported() bool { return false }

func newKernelDebugEnvironment(opts kernelDebugOptions) *kernelDebugEnvironment {
	return &kernelDebugEnvironment{opts: opts}
}

func (k *kernelDebugEnvironment) resetAtStart() []error { return nil }

func (k *kernelDebugEnvironment) enable() *kernelDebugLease {
	return &kernelDebugLease{begin: []error{errors.New("kernel AWG debug is only available on Linux")}}
}

func (k *kernelDebugEnvironment) startJournal(context.Context, time.Time) (kernelJournalProcess, error) {
	return nil, errors.New("the kernel log is only available on Linux")
}
