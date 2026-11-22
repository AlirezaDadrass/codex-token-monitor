package main

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// The hidden scheduled-task launcher owns the collector's lifetime. Task
// Scheduler can stop PowerShell without terminating its native child; observing
// the parent's process handle prevents orphan collectors after stop/upgrade.
func monitorParent(ctx context.Context, pid int, cancel context.CancelFunc) error {
	if pid <= 0 || uint64(pid) > 1<<32-1 {
		return errors.New("invalid parent process ID")
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		cancel() // The launcher already exited.
		return nil
	}
	if err != nil {
		return fmt.Errorf("monitor collector launcher: %w", err)
	}
	go func() {
		defer windows.CloseHandle(handle)
		for ctx.Err() == nil {
			state, err := windows.WaitForSingleObject(handle, 200)
			if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
				cancel()
				return
			}
		}
	}()
	return nil
}
