//go:build !linux

package tspuprobe

import (
	"context"
	"errors"
	"testing"
)

func TestRunIsUnsupportedOffLinux(t *testing.T) {
	if Supported() {
		t.Fatal("Supported() is true off Linux")
	}
	if _, err := Run(context.Background(), baseRequest()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
