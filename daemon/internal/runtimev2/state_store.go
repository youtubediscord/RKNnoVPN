package runtimev2

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const runtimeStateFileName = "runtime_state.json"

type RuntimeStateFile struct {
	DesiredGeneration int64               `json:"desiredGeneration"`
	AppliedGeneration int64               `json:"appliedGeneration"`
	ProfileHash       string              `json:"profileHash,omitempty"`
	LastVerifiedAt    string              `json:"lastVerifiedAt,omitempty"`
	LastHealthCode    string              `json:"lastHealthCode,omitempty"`
	LastHealthMessage string              `json:"lastHealthMessage,omitempty"`
	DesiredState      DesiredState        `json:"desiredState"`
	AppliedState      AppliedState        `json:"appliedState"`
	Health            HealthSnapshot      `json:"health"`
	Compatibility     CompatibilityStatus `json:"compatibility"`
	ActiveOperation   *OperationStatus    `json:"activeOperation,omitempty"`
	LastOperation     *OperationResult    `json:"lastOperation,omitempty"`
	UpdatedAt         string              `json:"updatedAt"`
}

func RuntimeStatePath(dataDir string) string {
	return filepath.Join(dataDir, "run", runtimeStateFileName)
}

func RuntimeStateFromStatus(status Status, now time.Time) RuntimeStateFile {
	desiredGeneration := status.AppliedState.Generation
	if status.ActiveOperation != nil {
		desiredGeneration = status.ActiveOperation.Generation
	} else if status.LastOperation != nil && status.LastOperation.Generation > desiredGeneration {
		desiredGeneration = status.LastOperation.Generation
	}
	lastVerifiedAt := ""
	if !status.Health.CheckedAt.IsZero() {
		lastVerifiedAt = status.Health.CheckedAt.Format(time.RFC3339)
	}
	return RuntimeStateFile{
		DesiredGeneration: desiredGeneration,
		AppliedGeneration: status.AppliedState.Generation,
		LastVerifiedAt:    lastVerifiedAt,
		LastHealthCode:    status.Health.LastCode,
		LastHealthMessage: firstNonEmpty(status.Health.LastUserMessage, status.Health.LastError),
		DesiredState:      status.DesiredState,
		AppliedState:      status.AppliedState,
		Health:            status.Health,
		Compatibility:     status.Compatibility,
		ActiveOperation:   status.ActiveOperation,
		LastOperation:     status.LastOperation,
		UpdatedAt:         now.Format(time.RFC3339),
	}
}

func WriteRuntimeState(dataDir string, status Status) error {
	path := RuntimeStatePath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir runtime state dir: %w", err)
	}
	data, err := json.MarshalIndent(RuntimeStateFromStatus(status, time.Now()), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime state: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-state-*")
	if err != nil {
		return fmt.Errorf("create runtime state temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write runtime state temp: %w", err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		return fmt.Errorf("chmod runtime state temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync runtime state temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close runtime state temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish runtime state: %w", err)
	}
	cleanup = false
	return nil
}

func ReadRuntimeState(dataDir string) (*RuntimeStateFile, error) {
	data, err := os.ReadFile(RuntimeStatePath(dataDir))
	if err != nil {
		return nil, err
	}
	var state RuntimeStateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
