//go:build !windows

package main

import (
	"context"
	"errors"
)

func monitorParent(_ context.Context, _ int, _ context.CancelFunc) error {
	return errors.New("--parent-pid is only supported by the Windows collector")
}
