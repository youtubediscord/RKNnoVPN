package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const installStateFileName = "update-install-state.json"

type InstallState struct {
	Status          string `json:"status"`
	Generation      int64  `json:"generation"`
	Step            string `json:"step,omitempty"`
	StepStatus      string `json:"step_status,omitempty"`
	Code            string `json:"code,omitempty"`
	Detail          string `json:"detail,omitempty"`
	ModulePath      string `json:"module_path,omitempty"`
	ApkPath         string `json:"apk_path,omitempty"`
	ApkInstalled    bool   `json:"apk_installed"`
	ModuleInstalled bool   `json:"module_installed"`
	StartedAt       string `json:"started_at"`
	UpdatedAt       string `json:"updated_at"`
}

type InstallTracker struct {
	path  string
	state InstallState
}

func NewInstallTracker(dataDir string, generation int64, modulePath string, apkPath string) *InstallTracker {
	now := time.Now().Format(time.RFC3339)
	return &InstallTracker{
		path: InstallStatePath(dataDir),
		state: InstallState{
			Status:     "running",
			Generation: generation,
			ModulePath: modulePath,
			ApkPath:    apkPath,
			StartedAt:  now,
			UpdatedAt:  now,
		},
	}
}

func InstallStatePath(dataDir string) string {
	return filepath.Join(dataDir, "run", installStateFileName)
}

func ReadInstallState(dataDir string) (*InstallState, error) {
	data, err := os.ReadFile(InstallStatePath(dataDir))
	if err != nil {
		return nil, err
	}
	var state InstallState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (t *InstallTracker) Begin() error {
	return t.write()
}

func (t *InstallTracker) Step(name, status, code, detail string) error {
	t.state.Step = name
	t.state.StepStatus = status
	t.state.Code = code
	t.state.Detail = detail
	if status == "failed" {
		t.state.Status = "failed"
	} else if t.state.Status != "completed" {
		t.state.Status = "running"
	}
	return t.write()
}

func (t *InstallTracker) MarkAPKInstalled() error {
	t.state.ApkInstalled = true
	return t.write()
}

func (t *InstallTracker) MarkModuleInstalled() error {
	t.state.ModuleInstalled = true
	return t.write()
}

func (t *InstallTracker) Complete() error {
	t.state.Status = "completed"
	t.state.StepStatus = "ok"
	t.state.Code = ""
	t.state.Detail = ""
	return t.write()
}

func (t *InstallTracker) write() error {
	t.state.UpdatedAt = time.Now().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(t.path), 0o750); err != nil {
		return fmt.Errorf("mkdir install state dir: %w", err)
	}
	data, err := json.MarshalIndent(t.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal install state: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(t.path), ".update-install-state-*")
	if err != nil {
		return fmt.Errorf("create install state temp: %w", err)
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
		return fmt.Errorf("write install state temp: %w", err)
	}
	if err := tmp.Chmod(0o640); err != nil {
		return fmt.Errorf("chmod install state temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync install state temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close install state temp: %w", err)
	}
	if err := os.Rename(tmpPath, t.path); err != nil {
		return fmt.Errorf("publish install state: %w", err)
	}
	cleanup = false
	return nil
}
