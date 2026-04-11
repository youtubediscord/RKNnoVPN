// Package watcher monitors the Android network subsystem for connectivity
// changes (Wi-Fi ↔ cellular, DHCP renewals, VPN toggles) and triggers
// re-application of proxy rules.
//
// On Android, /data/misc/net/ contains files updated by netd whenever the
// network configuration changes.  We use inotifyd (busybox) to watch that
// directory and call the net_handler.sh script on each event.
package watcher

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// watchDir is the Android directory that netd updates on network changes.
	watchDir = "/data/misc/net/"

	// debounceDelay prevents rapid-fire handler calls when netd writes
	// multiple files in quick succession during a single connectivity event.
	debounceDelay = 2 * time.Second
)

// NetworkWatcher monitors filesystem events under /data/misc/net/ via
// inotifyd and dispatches the net_handler.sh script on changes.
type NetworkWatcher struct {
	dataDir string // e.g. /data/adb/privstack
	env     map[string]string

	stopCh    chan struct{}
	done      chan struct{}
	cmd       *exec.Cmd
	logger    *log.Logger

	mu sync.Mutex
}

// NewNetworkWatcher creates a watcher that will invoke
// <dataDir>/scripts/net_handler.sh on network changes.
func NewNetworkWatcher(dataDir string, env map[string]string, logger *log.Logger) *NetworkWatcher {
	if logger == nil {
		logger = log.New(os.Stderr, "[netwatch] ", log.LstdFlags)
	}
	return &NetworkWatcher{
		dataDir: dataDir,
		env:     env,
		logger:  logger,
	}
}

// SetEnv updates the environment that will be used for future handler runs.
func (w *NetworkWatcher) SetEnv(env map[string]string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.env = env
}

// Start launches the inotifyd subprocess and a goroutine that reads its
// output. Each line of inotifyd output triggers handleNetworkChange.
func (w *NetworkWatcher) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.stopCh != nil {
		return fmt.Errorf("netwatch: already running")
	}

	// Verify the watch directory exists.
	if _, err := os.Stat(watchDir); err != nil {
		return fmt.Errorf("netwatch: %s not accessible: %w", watchDir, err)
	}

	// Locate inotifyd binary. On most Android devices busybox provides it.
	// inotifydArgs returns the binary path and any prefix arguments needed
	// (e.g. ["busybox", "inotifyd"] when the binary is busybox).
	binPath, prefixArgs, err := findInotifyd()
	if err != nil {
		return fmt.Errorf("netwatch: %w", err)
	}

	// inotifyd PROG DIR:MASK
	// We watch for create, delete, modify, and move events.
	// The "-" argument makes inotifyd print events to stdout instead of
	// calling a handler program (we read stdout in Go and debounce).
	args := append(prefixArgs, "-", watchDir+":wcdnm")
	w.cmd = exec.Command(binPath, args...)
	stdout, err := w.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("netwatch: stdout pipe: %w", err)
	}
	w.cmd.Stderr = os.Stderr

	if err := w.cmd.Start(); err != nil {
		return fmt.Errorf("netwatch: start inotifyd: %w", err)
	}

	w.stopCh = make(chan struct{})
	w.done = make(chan struct{})
	w.logger.Printf("watching %s via inotifyd (pid=%d)", watchDir, w.cmd.Process.Pid)

	go func() {
		defer close(w.done)
		scanner := bufio.NewScanner(stdout)
		var lastFire time.Time

		for scanner.Scan() {
			select {
			case <-w.stopCh:
				return
			default:
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			// Debounce: if we already fired recently, skip.
			if time.Since(lastFire) < debounceDelay {
				continue
			}
			lastFire = time.Now()

			w.logger.Printf("network change detected: %s", line)
			w.handleNetworkChange()
		}

		// scanner exits when inotifyd's stdout is closed.
		if err := scanner.Err(); err != nil {
			select {
			case <-w.stopCh:
				// Expected during shutdown.
			default:
				w.logger.Printf("inotifyd read error: %v", err)
			}
		}
	}()

	return nil
}

// Stop terminates the inotifyd subprocess and waits for the reader
// goroutine to exit.
func (w *NetworkWatcher) Stop() {
	w.mu.Lock()
	ch := w.stopCh
	w.stopCh = nil
	w.mu.Unlock()

	if ch == nil {
		return
	}
	close(ch)

	// Kill inotifyd — this also closes its stdout, which unblocks the
	// scanner goroutine.
	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
		_ = w.cmd.Wait()
	}

	<-w.done
	w.logger.Println("stopped")
}

// handleNetworkChange runs the net_handler.sh script with the configured
// environment. The script is expected to re-check routes and re-apply
// iptables if the active network interface changed.
func (w *NetworkWatcher) handleNetworkChange() {
	scriptPath := filepath.Join(w.dataDir, "scripts", "net_handler.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		w.logger.Printf("net_handler.sh not found at %s: %v", scriptPath, err)
		return
	}

	shell := "/system/bin/sh"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell, scriptPath)
	cmd.Env = os.Environ()
	for k, v := range w.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		w.logger.Printf("net_handler.sh failed: %v\noutput: %s", err, strings.TrimSpace(string(out)))
		return
	}
	w.logger.Println("net_handler.sh completed successfully")
}

// findInotifyd locates the inotifyd binary and returns the binary path
// plus any prefix arguments needed for exec.Command.
//
// For a standalone binary: ("/system/bin/inotifyd", nil, nil)
// For a busybox applet:   ("/data/adb/magisk/busybox", ["inotifyd"], nil)
func findInotifyd() (bin string, prefixArgs []string, err error) {
	// Check PATH first.
	if path, lookErr := exec.LookPath("inotifyd"); lookErr == nil {
		return path, nil, nil
	}

	// Common Android locations.
	candidates := []string{
		"/system/bin/inotifyd",
		"/system/xbin/inotifyd",
		"/data/adb/magisk/busybox",
		"/data/adb/ksu/bin/busybox",
		"/data/adb/ap/bin/busybox",
	}

	for _, c := range candidates {
		if _, statErr := os.Stat(c); statErr != nil {
			continue
		}
		// If it is busybox, verify the inotifyd applet is compiled in.
		if strings.Contains(c, "busybox") {
			cmd := exec.Command(c, "--list")
			out, listErr := cmd.Output()
			if listErr == nil && strings.Contains(string(out), "inotifyd") {
				return c, []string{"inotifyd"}, nil
			}
			continue
		}
		return c, nil, nil
	}

	return "", nil, fmt.Errorf("inotifyd not found in PATH or common locations")
}
