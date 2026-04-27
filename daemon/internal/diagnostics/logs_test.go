package diagnostics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLogFileSpecsDeduplicatesAndDefaults(t *testing.T) {
	dataDir := "/data/adb/modules/rknnovpn"

	specs := ResolveLogFileSpecs(dataDir, []string{"singbox", "daemon", "sing-box", "unknown"})
	if len(specs) != 2 {
		t.Fatalf("expected daemon and sing-box specs, got %#v", specs)
	}
	if specs[0].Name != "sing-box" || specs[1].Name != "daemon" {
		t.Fatalf("unexpected spec order/content: %#v", specs)
	}

	specs = ResolveLogFileSpecs(dataDir, []string{"unknown"})
	if len(specs) != 1 || specs[0].Name != "daemon" {
		t.Fatalf("unknown request should default to daemon log, got %#v", specs)
	}
}

func TestReadLogSectionsRedactsAndReportsMissing(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(logPath, []byte("one\nsecret\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	sections := ReadLogSections([]LogFileSpec{
		{Name: "daemon", Path: logPath},
		{Name: "missing", Path: filepath.Join(dir, "missing.log")},
	}, 2, 1024, func(line string) string {
		return strings.ReplaceAll(line, "secret", "[redacted]")
	})

	if len(sections) != 2 {
		t.Fatalf("expected two sections, got %#v", sections)
	}
	if got := strings.Join(sections[0].Lines, ","); got != "[redacted],three" {
		t.Fatalf("unexpected redacted tail: %q", got)
	}
	if !sections[1].Missing {
		t.Fatalf("missing log should be marked missing: %#v", sections[1])
	}
}

func TestReadLogTailDropsPartialPrefixWhenByteLimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	content := strings.Join([]string{"one", "two", "three", "four"}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	lines, err := ReadLogTail(path, 10, 11)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, ","); got != "three,four" {
		t.Fatalf("unexpected byte-limited tail: %q", got)
	}
}
