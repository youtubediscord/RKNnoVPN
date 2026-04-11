package updater

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Default filesystem paths used by the Magisk module layout.
const (
	DefaultDataDir   = "/data/adb/privstack"
	DefaultModuleDir = "/data/adb/modules/privstack"
)

// --------------------------------------------------------------------------
// InstallModuleUpdate
// --------------------------------------------------------------------------

// InstallModuleUpdate performs a hot-install of a new module.zip WITHOUT
// requiring a reboot. The sequence is:
//
//  1. Stop the current proxy (sing-box + iptables teardown)
//  2. Extract the new module.zip to a temp staging directory
//  3. Copy new binaries to <dataDir>/bin/
//  4. Copy new scripts to <dataDir>/scripts/
//  5. Update module files in <moduleDir>/
//  6. Set correct permissions
//  7. Re-launch the updated privd daemon (exec self-replace)
//
// If any step fails the function returns an error, and the caller should
// attempt recovery (e.g. restart the old daemon).
func InstallModuleUpdate(zipPath string, dataDir string, moduleDir string) error {
	logger := log.New(log.Writer(), "[updater] ", log.LstdFlags)

	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	if moduleDir == "" {
		moduleDir = DefaultModuleDir
	}

	// --- 1. Stop the current proxy ---
	logger.Println("stopping current proxy before module update")
	if err := stopCurrentProxy(dataDir); err != nil {
		logger.Printf("warning: stop proxy: %v (continuing anyway)", err)
	}

	// --- 2. Extract zip to staging ---
	staging, err := os.MkdirTemp(dataDir, "update-staging-*")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	logger.Printf("extracting %s to %s", zipPath, staging)
	if err := extractZip(zipPath, staging); err != nil {
		return fmt.Errorf("extract zip: %w", err)
	}

	// --- 3. Copy binaries ---
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0750); err != nil {
		return fmt.Errorf("mkdir bin: %w", err)
	}

	// Binaries may be in <staging>/binaries/arm64/ or directly in <staging>/.
	stagedBinDir := findSubdir(staging, "binaries", "arm64")
	if stagedBinDir == "" {
		stagedBinDir = staging
	}

	for _, name := range []string{"sing-box", "privd", "privctl"} {
		src := filepath.Join(stagedBinDir, name)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue // binary not in this release
		}
		dst := filepath.Join(binDir, name)
		logger.Printf("installing binary %s", name)
		if err := copyFile(src, dst, 0750); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}

	// --- 4. Copy scripts ---
	scriptsDir := filepath.Join(dataDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		return fmt.Errorf("mkdir scripts: %w", err)
	}

	stagedScriptsDir := filepath.Join(staging, "scripts")
	if info, err := os.Stat(stagedScriptsDir); err == nil && info.IsDir() {
		logger.Println("updating scripts")
		if err := copyDir(stagedScriptsDir, scriptsDir, 0755); err != nil {
			return fmt.Errorf("copy scripts: %w", err)
		}
	}

	// --- 5. Update module directory ---
	logger.Println("updating module files")
	moduleFiles := []string{
		"module.prop",
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"sepolicy.rule",
	}
	for _, name := range moduleFiles {
		src := filepath.Join(staging, name)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		dst := filepath.Join(moduleDir, name)
		perm := os.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			perm = 0755
		}
		if err := copyFile(src, dst, perm); err != nil {
			return fmt.Errorf("copy module file %s: %w", name, err)
		}
	}

	// Copy defaults/ directory if present.
	stagedDefaults := filepath.Join(staging, "defaults")
	if info, err := os.Stat(stagedDefaults); err == nil && info.IsDir() {
		moduleDefaults := filepath.Join(moduleDir, "defaults")
		if err := copyDir(stagedDefaults, moduleDefaults, 0644); err != nil {
			logger.Printf("warning: copy defaults: %v", err)
		}
	}

	// --- 6. Set permissions ---
	logger.Println("setting permissions")
	setPerms(binDir, 0750)
	setPerms(scriptsDir, 0755)

	// --- 7. Re-launch updated privd ---
	logger.Println("re-launching privd")
	if err := relaunchDaemon(dataDir); err != nil {
		return fmt.Errorf("relaunch daemon: %w", err)
	}

	logger.Println("module update installed successfully")
	return nil
}

// --------------------------------------------------------------------------
// InstallApkUpdate
// --------------------------------------------------------------------------

// InstallApkUpdate installs the APK using the Android package manager.
// Requires root privileges (the daemon runs as root).
func InstallApkUpdate(apkPath string) error {
	cmd := exec.Command("pm", "install", "-r", apkPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pm install: %s: %w", string(output), err)
	}
	return nil
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// stopCurrentProxy invokes the iptables teardown script and kills sing-box.
func stopCurrentProxy(dataDir string) error {
	// First, remove iptables rules so traffic is not black-holed.
	dnsScript := filepath.Join(dataDir, "scripts", "dns.sh")
	_ = execScript(dnsScript, "stop")

	iptScript := filepath.Join(dataDir, "scripts", "iptables.sh")
	_ = execScript(iptScript, "stop")

	// Kill sing-box.
	pidFile := filepath.Join(dataDir, "run", "singbox.pid")
	if data, err := os.ReadFile(pidFile); err == nil {
		pid := strings.TrimSpace(string(data))
		_ = exec.Command("kill", "-TERM", pid).Run()
		time.Sleep(2 * time.Second)
		_ = exec.Command("kill", "-KILL", pid).Run()
	}

	// Kill the current privd (we will be replaced anyway).
	privdPid := filepath.Join(dataDir, "run", "daemon.pid")
	if data, err := os.ReadFile(privdPid); err == nil {
		pid := strings.TrimSpace(string(data))
		// Don't kill ourselves -- the IPC handler is running under this PID.
		// The relaunch step will exec-replace us.
		_ = pid
	}

	return nil
}

// relaunchDaemon starts the new privd binary. It uses exec to replace the
// current process if running as the daemon, or spawns a new one.
func relaunchDaemon(dataDir string) error {
	privdBin := filepath.Join(dataDir, "bin", "privd")
	configPath := filepath.Join(dataDir, "config.json")
	logFile := filepath.Join(dataDir, "log", "daemon.log")
	pidFile := filepath.Join(dataDir, "run", "daemon.pid")

	// Fork a new daemon process rather than exec-replacing, because the
	// IPC handler needs to return a response first.
	cmd := exec.Command(privdBin,
		"-config", configPath,
		"-data-dir", dataDir,
		"-log-file", logFile,
		"-pid-file", pidFile,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // detach from our process group
	}
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn new privd: %w", err)
	}

	// Release the child so it doesn't become a zombie when we exit.
	_ = cmd.Process.Release()
	return nil
}

// execScript runs a shell script with an action argument.
func execScript(scriptPath string, action string) error {
	cmd := exec.Command("sh", scriptPath, action)
	// Inherit environment so PRIVSTACK_DIR etc. are available.
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s: %w", scriptPath, action, string(output), err)
	}
	return nil
}

// extractZip extracts all files from a ZIP archive into destDir.
func extractZip(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(destDir, f.Name)

		// Guard against zip-slip.
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies src to dst, setting the given file permissions.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(perm)
}

// copyDir copies all files from srcDir into dstDir, creating it if needed.
func copyDir(srcDir, dstDir string, filePerm os.FileMode) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, _ := filepath.Rel(srcDir, path)
		target := filepath.Join(dstDir, relPath)

		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		return copyFile(path, target, filePerm)
	})
}

// findSubdir checks if the path <base>/<a>/<b> exists and returns it, or "".
func findSubdir(base string, parts ...string) string {
	p := base
	for _, part := range parts {
		p = filepath.Join(p, part)
	}
	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return p
	}
	return ""
}

// setPerms walks a directory and sets the given mode on regular files.
func setPerms(dir string, perm os.FileMode) {
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		os.Chmod(path, perm)
		return nil
	})
}
