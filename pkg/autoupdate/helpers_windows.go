//go:build windows
// +build windows

package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// checkExecutablePermissions checks wehether a specific file looks
// like it's executable. This is used in evaluating whether something
// is an updated version.
//
// Windows does not have executable bits, so we omit those. And
// instead check the file extension.
func checkExecutablePermissions(potentialBinary string) error {
	if potentialBinary == "" {
		return errors.New("empty string isn't executable")
	}
	stat, err := os.Stat(potentialBinary)
	switch {
	case os.IsNotExist(err):
		return errors.New("No such file")
	case stat.IsDir():
		return errors.New("is a directory")
	case err != nil:
		return fmt.Errorf("statting file: %w", err)
	case !strings.HasSuffix(potentialBinary, ".exe"):
		return errors.New("not executable")
	}

	return nil
}
