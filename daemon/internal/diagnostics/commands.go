package diagnostics

import (
	"os"
	"strings"
)

type ExecCommandFunc func(name string, args ...string) (string, error)

type FileStatus struct {
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Executable bool   `json:"executable,omitempty"`
	Mode       string `json:"mode,omitempty"`
	Error      string `json:"error,omitempty"`
}

func RunCommand(maxLines int, exec ExecCommandFunc, name string, args ...string) CommandResult {
	command := strings.Join(append([]string{name}, args...), " ")
	result := CommandResult{Command: command}
	if exec == nil {
		result.Error = "command executor is not configured"
		return result
	}
	out, err := exec(name, args...)
	result.Lines = limitLines(splitLines(RedactText(out)), maxLines)
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func StatFile(path string, executable bool) FileStatus {
	status := FileStatus{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			status.Error = err.Error()
		}
		return status
	}
	status.Exists = true
	status.Mode = info.Mode().Perm().String()
	if executable {
		status.Executable = info.Mode().Perm()&0111 != 0
	}
	return status
}
