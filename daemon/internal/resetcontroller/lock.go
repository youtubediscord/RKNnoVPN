package resetcontroller

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/runtimev2"
)

const StaleAfter = 10 * time.Minute

type LockInfo struct {
	Active    bool
	Stale     bool
	Detail    string
	StartedAt time.Time
	Age       time.Duration
}

type StaleRecoveryDecision struct {
	RunCleanup bool
	Detail     string
}

func InspectLock(paths Paths, now time.Time) (LockInfo, error) {
	info, err := os.Stat(paths.ResetLock())
	if err != nil {
		if os.IsNotExist(err) {
			return LockInfo{}, nil
		}
		return LockInfo{}, fmt.Errorf("reset lock is not readable: %w", err)
	}

	startedAt := info.ModTime()
	if data, readErr := os.ReadFile(paths.ResetLock()); readErr == nil {
		text := strings.TrimSpace(string(data))
		if parsed, parseErr := time.Parse(time.RFC3339, text); parseErr == nil {
			startedAt = parsed
		}
	}

	age := now.Sub(startedAt)
	if age < 0 {
		age = 0
	}
	return LockInfo{
		Active:    true,
		Stale:     age > StaleAfter,
		Detail:    fmt.Sprintf("reset lock is present for %s", age.Truncate(time.Second)),
		StartedAt: startedAt,
		Age:       age,
	}, nil
}

func DecideStaleRecovery(paths Paths, now time.Time) (StaleRecoveryDecision, error) {
	lock, err := InspectLock(paths, now)
	if err != nil {
		return StaleRecoveryDecision{}, err
	}
	if !lock.Active {
		return StaleRecoveryDecision{}, nil
	}
	if !lock.Stale {
		return StaleRecoveryDecision{}, runtimev2.NewResetInProgressError("reset is in progress")
	}
	return StaleRecoveryDecision{RunCleanup: true, Detail: lock.Detail}, nil
}

func FailIfResetInProgress(paths Paths, now time.Time) error {
	lock, err := InspectLock(paths, now)
	if err != nil {
		return err
	}
	if !lock.Active {
		return nil
	}
	if lock.Stale {
		return runtimev2.NewResetInProgressError(lock.Detail)
	}
	return runtimev2.NewResetInProgressError("reset is in progress")
}

func ShouldSkipRootReconcile(paths Paths) (bool, string) {
	if _, err := os.Stat(paths.ResetLock()); err == nil {
		return true, "reset lock is present"
	}
	if _, err := os.Stat(paths.ManualFlag()); err == nil {
		return true, "manual mode is enabled"
	}
	if _, err := os.Stat(paths.ActiveMarker()); err != nil {
		if os.IsNotExist(err) {
			return true, "runtime is not marked active"
		}
		return true, "active marker is not readable: " + err.Error()
	}
	return false, ""
}
