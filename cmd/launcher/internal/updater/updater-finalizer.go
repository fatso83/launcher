//go:build !windows
// +build !windows

package updater

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/kolide/launcher/pkg/autoupdate"
	"github.com/kolide/launcher/pkg/contexts/ctxlog"
)

// UpdateFinalizer finalizes a launcher update. It assume the new
// binary has been copied into place, and calls exec, so we start a
// new running launcher in our place.
func UpdateFinalizer(logger log.Logger, shutdownOsquery func() error) func() error {
	return func() error {
		if err := shutdownOsquery(); err != nil {
			level.Info(logger).Log("method", "updateFinalizer", "err", err)
			level.Debug(logger).Log("method", "updateFinalizer", "err", err, "stack", fmt.Sprintf("%+v", err))
		}
		// find the newest version of launcher on disk.
		// FindNewestSelf uses context as a way to get a
		// logger, so we need to create and pass one.
		binaryPath, err := autoupdate.FindNewestSelf(
			ctxlog.NewContext(context.TODO(), logger),
			autoupdate.DeleteCorruptUpdates(),
			autoupdate.DeleteOldUpdates(),
		)

		if err != nil {
			level.Info(logger).Log("method", "updateFinalizer", "err", err)
			return fmt.Errorf("finding newest: %w", err)
		}

		// replace launcher
		level.Info(logger).Log(
			"msg", "Exec updated launcher",
			"newPath", binaryPath,
		)
		if err := syscall.Exec(binaryPath, os.Args, os.Environ()); err != nil {
			return fmt.Errorf("exec updated launcher: %w", err)
		}
		return nil
	}
}
