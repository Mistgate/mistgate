//go:build !linux

package main

import "github.com/mistgate/mistgate/internal/node/awgprep"

func newAwgKmod(string) *awgprep.KmodController { return nil }
