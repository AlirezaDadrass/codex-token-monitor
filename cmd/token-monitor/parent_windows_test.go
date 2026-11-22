package main

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCollectorParentHelper(t *testing.T) {
	if os.Getenv("TOKEN_MONITOR_PARENT_TEST_HELPER") == "1" {
		time.Sleep(30 * time.Second)
	}
}

func TestCollectorExitsWhenLauncherIsTerminated(t *testing.T) {
	parent := exec.Command(os.Args[0], "-test.run=^TestCollectorParentHelper$")
	parent.Env = append(os.Environ(), "TOKEN_MONITOR_PARENT_TEST_HELPER=1")
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Process.Kill(); _ = parent.Wait() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := monitorParent(ctx, parent.Process.Pid, cancel); err != nil {
		t.Fatal(err)
	}
	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("collector context survived its launcher")
	}
}
