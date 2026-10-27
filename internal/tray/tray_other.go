//go:build !windows

package tray

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Run on non-Windows environments waits for standard OS interrupt signals
func Run(ctx context.Context, port int, onExit func()) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigChan:
	case <-ctx.Done():
	}

	if onExit != nil {
		onExit()
	}
	return nil
}
