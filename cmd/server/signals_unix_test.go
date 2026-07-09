//go:build !windows

package main

import (
	"os/signal"
	"syscall"
	"testing"
)

func TestShutdownSignalsExcludeSIGHUP(t *testing.T) {
	for _, sig := range shutdownSignals {
		if sig == syscall.SIGHUP {
			t.Fatal("SIGHUP must not shut down the dev server; terminal hangups should not look like crashes")
		}
	}
}

func TestSIGHUPIgnored(t *testing.T) {
	if !signal.Ignored(syscall.SIGHUP) {
		t.Fatal("SIGHUP must be ignored so terminal hangups do not stop the server")
	}
}
