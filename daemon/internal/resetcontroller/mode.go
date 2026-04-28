package resetcontroller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func EnterResetMode(paths Paths, now time.Time) error {
	if err := os.MkdirAll(paths.RunDir(), 0o750); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.ConfigDir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(paths.ResetLock(), []byte(now.Format(time.RFC3339)+"\n"), 0o640); err != nil {
		return err
	}
	_ = os.Remove(paths.ActiveMarker())
	if err := os.WriteFile(paths.ManualFlag(), []byte("network reset\n"), 0o600); err != nil {
		return err
	}
	return nil
}

func LeaveResetMode(paths Paths) error {
	if err := os.Remove(paths.ResetLock()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func RemoveStaleRuntimeFiles(paths Paths) ([]string, error) {
	removed := make([]string, 0)
	errs := make([]string, 0)
	for _, path := range paths.StaleRuntimeFiles() {
		if err := os.Remove(path); err == nil {
			removed = append(removed, filepath.Base(path))
		} else if !os.IsNotExist(err) {
			errs = append(errs, filepath.Base(path)+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return removed, nil
}

func IsIgnorableResetScriptError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "script not found:") ||
		strings.Contains(lower, "no such file or directory")
}
