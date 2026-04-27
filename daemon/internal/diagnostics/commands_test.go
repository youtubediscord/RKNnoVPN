package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCommandRedactsAndLimitsOutput(t *testing.T) {
	result := RunCommand(2, func(name string, args ...string) (string, error) {
		if name != "tool" || strings.Join(args, " ") != "arg" {
			t.Fatalf("unexpected command args: %s %#v", name, args)
		}
		return "line1\nuuid=00000000-0000-0000-0000-000000000000\nline3\n", nil
	}, "tool", "arg")

	if result.Command != "tool arg" {
		t.Fatalf("unexpected command string: %#v", result)
	}
	if len(result.Lines) != 2 || result.Lines[0] != `uuid="[redacted]"` || result.Lines[1] != "line3" {
		t.Fatalf("unexpected limited/redacted lines: %#v", result.Lines)
	}
}

func TestRunCommandReportsExecutorErrors(t *testing.T) {
	expectedErr := errors.New("boom")
	result := RunCommand(10, func(string, ...string) (string, error) {
		return "partial output", expectedErr
	}, "tool")
	if result.Error != expectedErr.Error() || result.Lines[0] != "partial output" {
		t.Fatalf("unexpected command error result: %#v", result)
	}

	missing := RunCommand(10, nil, "tool")
	if missing.Error == "" {
		t.Fatalf("missing executor should be visible: %#v", missing)
	}
}

func TestStatFileReportsExistenceAndExecutableBit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/system/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	status := StatFile(path, true)
	if !status.Exists || !status.Executable || status.Mode != "-rwxr-xr-x" {
		t.Fatalf("unexpected executable file status: %#v", status)
	}

	missing := StatFile(filepath.Join(t.TempDir(), "missing"), true)
	if missing.Exists || missing.Error != "" {
		t.Fatalf("missing file should be cleanly reported: %#v", missing)
	}
}
