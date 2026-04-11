package core

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ExecScript runs a shell script with a single positional argument (typically
// "start" or "stop") and optional environment variables injected from env.
//
// The script is executed with /system/bin/sh (Android's default shell).
// If /system/bin/sh is absent, we fall back to /bin/sh.
func ExecScript(scriptPath string, command string, env map[string]string) error {
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("script not found: %s: %w", scriptPath, err)
	}

	shell := "/system/bin/sh"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, scriptPath, command)

	// Inherit the current environment, then layer the caller's overrides.
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Capture combined output for error reporting.
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("exec %s %s: %w\noutput: %s",
			scriptPath, command, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ExecIptables is a convenience wrapper that runs a single iptables command
// with the -w (wait-for-lock) flag so concurrent callers do not race.
//
//	ExecIptables("-t", "mangle", "-C", "PREROUTING", "-j", "PRIVSTACK_PRE")
//
// is equivalent to:
//
//	iptables -w 100 -t mangle -C PREROUTING -j PRIVSTACK_PRE
func ExecIptables(args ...string) error {
	fullArgs := append([]string{"-w", "100"}, args...)
	cmd := exec.Command("iptables", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %s: %w\noutput: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ExecIp6tables is the IPv6 counterpart of ExecIptables.
func ExecIp6tables(args ...string) error {
	fullArgs := append([]string{"-w", "100"}, args...)
	cmd := exec.Command("ip6tables", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ip6tables %s: %w\noutput: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// WaitForPort blocks until a TCP connection to host:port succeeds or the
// timeout elapses. It polls every 250 ms.
func WaitForPort(host string, port int, timeout time.Duration) error {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("port %s not listening after %s", addr, timeout)
}

// ExecCommand runs an arbitrary command and returns its combined output.
// It is used by health checks that need to inspect command output (e.g.
// ip rule show, iptables -C ...).
func ExecCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ResolvePackageUIDs reads /data/system/packages.list and resolves
// package names to Android UIDs. Returns space-separated UID string.
//
// packages.list format: package_name uid flags data_dir seinfo gids
// For multi-user devices, UIDs for additional profiles are computed as
// user_id * 100000 + app_id (where app_id = uid % 100000).
func ResolvePackageUIDs(packages []string) string {
	if len(packages) == 0 {
		return ""
	}

	// Build a lookup set for O(1) matching.
	wanted := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		wanted[pkg] = true
	}

	data, err := os.ReadFile("/data/system/packages.list")
	if err != nil {
		return ""
	}

	// Discover active user IDs from /data/user/ directories.
	// User 0 is always present; additional profiles appear as /data/user/<id>.
	userIDs := []int{0}
	entries, err := os.ReadDir("/data/user")
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if uid, parseErr := strconv.Atoi(e.Name()); parseErr == nil && uid > 0 {
				userIDs = append(userIDs, uid)
			}
		}
	}

	var uids []string
	seen := make(map[int]bool)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pkgName := fields[0]
		if !wanted[pkgName] {
			continue
		}
		appUID, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil {
			continue
		}
		appID := appUID % 100000

		// Emit UIDs for all active user profiles.
		for _, userID := range userIDs {
			fullUID := userID*100000 + appID
			if !seen[fullUID] {
				seen[fullUID] = true
				uids = append(uids, strconv.Itoa(fullUID))
			}
		}
	}

	return strings.Join(uids, " ")
}

// MapAppMode converts config apps.mode values to the shell-script APP_MODE
// values expected by iptables.sh.
func MapAppMode(mode string) string {
	switch mode {
	case "whitelist", "include":
		return "whitelist"
	case "blacklist", "exclude":
		return "blacklist"
	case "all":
		return "all"
	default:
		return "all"
	}
}
