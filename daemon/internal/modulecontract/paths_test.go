package modulecontract

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultPathsMirrorShellEnvContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	envPath := filepath.Join(repoRoot, "module", "scripts", "lib", "rknnovpn_env.sh")
	content, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env contract: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		`/data/adb/modules/rknnovpn`,
		`RUN_DIR="${RUN_DIR:-${RKNNOVPN_DIR}/run}"`,
		`RESET_LOCK="${RESET_LOCK:-${RUN_DIR}/reset.lock}"`,
		`ACTIVE_FILE="${ACTIVE_FILE:-${RUN_DIR}/active}"`,
		`MANUAL_FLAG="${MANUAL_FLAG:-${CONFIG_DIR}/manual}"`,
		`DAEMON_SOCK="${DAEMON_SOCK:-${RUN_DIR}/daemon.sock}"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("shell env contract missing %q", want)
		}
	}

	paths := NewPaths("")
	if paths.Dir() != DefaultModuleDir {
		t.Fatalf("default dir = %q", paths.Dir())
	}
	if paths.ResetLock() != filepath.Join(DefaultModuleDir, "run", "reset.lock") {
		t.Fatalf("reset lock = %q", paths.ResetLock())
	}
	if paths.ActiveFile() != filepath.Join(DefaultModuleDir, "run", "active") {
		t.Fatalf("active file = %q", paths.ActiveFile())
	}
	if paths.ManualFlag() != filepath.Join(DefaultModuleDir, "config", "manual") {
		t.Fatalf("manual flag = %q", paths.ManualFlag())
	}
	if paths.DaemonSocket() != filepath.Join(DefaultModuleDir, "run", "daemon.sock") {
		t.Fatalf("daemon socket = %q", paths.DaemonSocket())
	}
}
