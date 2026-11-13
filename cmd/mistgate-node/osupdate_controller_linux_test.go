//go:build linux

package main

import (
	"testing"

	"github.com/mistgate/mistgate/internal/node/awgprep"
)

func TestKmodUnitRefusalExplainsAfterAptWait(t *testing.T) {
	if got := kmodUnitRefusal(awgprep.KmodAfterApt, true); got != "waiting for the package manager" {
		t.Fatalf("after-apt refusal = %q", got)
	}
	if got := kmodUnitRefusal(awgprep.KmodRepair, true); got != "the module rebuild is running" {
		t.Fatalf("repair refusal = %q", got)
	}
}
