//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

func init() {
	signal.Ignore(syscall.SIGHUP)
}

var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
