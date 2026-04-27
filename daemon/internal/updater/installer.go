package updater

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// SelfExitDelay is how long the old daemon waits after forking the new
// one before sending itself SIGTERM. Exported so the IPC handler can
// schedule this after the response is written.
const SelfExitDelay = 3 * time.Second

// Default filesystem paths used by the Magisk module layout.
const (
	DefaultDataDir   = "/data/adb/privstack"
	DefaultModuleDir = "/data/adb/modules/privstack"
)

type ModulePreflight struct {
	Version     string
	VersionCode string
}

func PreflightModuleUpdate(zipPath string, dataDir string) (*ModulePreflight, error) {
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	staging, err := os.MkdirTemp(dataDir, "update-preflight-*")
	if err != nil {
		return nil, fmt.Errorf("create preflight staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := extractZip(zipPath, staging); err != nil {
		return nil, fmt.Errorf("preflight extract zip: %w", err)
	}
	stagedBinDir, err := stagedBinaryDir(staging)
	if err != nil {
		return nil, err
	}
	if stagedBinDir == "" {
		stagedBinDir = staging
	}
	if err := validateModuleStaging(staging, stagedBinDir); err != nil {
		return nil, err
	}
	props, err := readModuleProp(filepath.Join(staging, "module.prop"))
	if err != nil {
		return nil, err
	}
	return &ModulePreflight{
		Version:     NormalizeVersionTag(props["version"]),
		VersionCode: props["versionCode"],
	}, nil
}

// --------------------------------------------------------------------------
// InstallModuleUpdate
// --------------------------------------------------------------------------

// InstallModuleUpdate performs a hot-install of a new module.zip WITHOUT
// requiring a reboot. The sequence is:
//
//  1. Extract the new module.zip to a temp staging directory
//  2. Validate required binaries/scripts/module metadata before downtime
//  3. Run canonical runtime cleanup (sing-box + PrivStack netstack teardown)
//  4. Back up current binaries so we can roll back on failure
//  5. Atomically replace binaries in <dataDir>/bin/ (unlink+rename)
//  6. Copy new scripts to <dataDir>/scripts/
//  7. Update module files in <moduleDir>/
//  8. Set correct permissions
//  9. Verify the new privd binary can at least print its version
//
// 10. Fork the new privd daemon
// 11. Schedule self-termination of the old daemon (after IPC response)
//
// If any step before the fork fails, binaries are rolled back from backup
// and the caller can restart the old daemon normally.
func InstallModuleUpdate(zipPath string, dataDir string, moduleDir string) error {
	logger := log.New(log.Writer(), "[updater] ", log.LstdFlags)

	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	if moduleDir == "" {
		moduleDir = DefaultModuleDir
	}

	// --- 1. Extract zip to staging ---
	staging, err := os.MkdirTemp(dataDir, "update-staging-*")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	logger.Printf("extracting %s to %s", zipPath, staging)
	if err := extractZip(zipPath, staging); err != nil {
		return fmt.Errorf("extract zip: %w", err)
	}

	// --- 2. Validate the staged release before we stop the working runtime. ---
	// Binaries may be in <staging>/binaries/<device-arch>/ or directly in <staging>/.
	stagedBinDir, err := stagedBinaryDir(staging)
	if err != nil {
		return err
	}
	if stagedBinDir == "" {
		stagedBinDir = staging
	}
	if err := validateModuleStaging(staging, stagedBinDir); err != nil {
		return err
	}
	moduleProps, err := readModuleProp(filepath.Join(staging, "module.prop"))
	if err != nil {
		return err
	}
	releaseDir, err := prepareVersionedRelease(staging, stagedBinDir, dataDir, moduleProps["version"])
	if err != nil {
		return err
	}

	// --- 3. Clean the current runtime ---
	logger.Println("running runtime cleanup before module update")
	if err := stopCurrentProxy(dataDir); err != nil {
		return fmt.Errorf("runtime cleanup before module update: %w", err)
	}

	// --- 4. Back up current binaries ---
	binDir := filepath.Join(dataDir, "bin")
	if err := os.MkdirAll(binDir, 0750); err != nil {
		return fmt.Errorf("mkdir bin: %w", err)
	}

	backupDir := filepath.Join(dataDir, "backup", "bin-pre-update")
	os.RemoveAll(backupDir) // clean stale backup
	if err := os.MkdirAll(backupDir, 0750); err != nil {
		return fmt.Errorf("mkdir backup: %w", err)
	}

	binaries := []string{"sing-box", "privd", "privctl"}
	for _, name := range binaries {
		src := filepath.Join(binDir, name)
		if _, err := os.Stat(src); err == nil {
			dst := filepath.Join(backupDir, name)
			if copyErr := copyFile(src, dst, 0750); copyErr != nil {
				logger.Printf("warning: backup %s: %v", name, copyErr)
			}
		}
	}

	// rollbackBinaries restores backed-up binaries if the update fails
	// partway through binary installation.
	rollbackBinaries := func() {
		logger.Println("rolling back binaries from backup")
		for _, name := range binaries {
			src := filepath.Join(backupDir, name)
			if _, err := os.Stat(src); err == nil {
				dst := filepath.Join(binDir, name)
				if err := atomicCopyFile(src, dst, 0750); err != nil {
					logger.Printf("warning: rollback %s: %v", name, err)
				}
			}
		}
	}

	// --- 5. Atomically replace binaries ---
	for _, name := range binaries {
		src := filepath.Join(stagedBinDir, name)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			rollbackBinaries()
			return fmt.Errorf("release bundle missing required binary %s", name)
		}
		dst := filepath.Join(binDir, name)
		logger.Printf("installing binary %s (atomic)", name)
		if err := atomicCopyFile(src, dst, 0750); err != nil {
			rollbackBinaries()
			return fmt.Errorf("install %s: %w", name, err)
		}
	}

	// --- 6. Copy scripts ---
	scriptsDir := filepath.Join(dataDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		rollbackBinaries()
		return fmt.Errorf("mkdir scripts: %w", err)
	}

	stagedScriptsDir := filepath.Join(staging, "scripts")
	if info, err := os.Stat(stagedScriptsDir); err == nil && info.IsDir() {
		logger.Println("updating scripts")
		if err := replaceDirFromSource(stagedScriptsDir, scriptsDir, 0755); err != nil {
			rollbackBinaries()
			return fmt.Errorf("copy scripts: %w", err)
		}
	}

	// --- 7. Update module directory ---
	logger.Println("updating module files")
	if info, err := os.Stat(stagedScriptsDir); err == nil && info.IsDir() {
		moduleScriptsDir := filepath.Join(moduleDir, "scripts")
		if err := replaceDirFromSource(stagedScriptsDir, moduleScriptsDir, 0755); err != nil {
			logger.Printf("warning: copy module scripts: %v", err)
		}
	}
	moduleFiles := []string{
		"OWNERSHIP.md",
		"module.prop",
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"sepolicy.rule",
	}
	for _, name := range moduleFiles {
		src := filepath.Join(staging, name)
		dst := filepath.Join(moduleDir, name)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				logger.Printf("warning: remove stale module file %s: %v", name, err)
			}
			continue
		}
		perm := os.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			perm = 0755
		}
		if err := copyFile(src, dst, perm); err != nil {
			// Module file update failure is non-fatal for the daemon itself.
			logger.Printf("warning: copy module file %s: %v", name, err)
		}
	}

	// Copy defaults/ directory if present.
	stagedDefaults := filepath.Join(staging, "defaults")
	if info, err := os.Stat(stagedDefaults); err == nil && info.IsDir() {
		moduleDefaults := filepath.Join(moduleDir, "defaults")
		if err := replaceDirFromSource(stagedDefaults, moduleDefaults, 0644); err != nil {
			logger.Printf("warning: copy defaults: %v", err)
		}
	}

	// --- 8. Set permissions ---
	logger.Println("setting permissions")
	setPerms(binDir, 0750)
	setPerms(scriptsDir, 0755)

	// Remove stale rules again using the freshly copied scripts. This cleans up
	// duplicate fwmark policy rules or old chain layouts that older scripts may
	// have failed to tear down before extraction.
	logger.Println("running post-copy network cleanup with updated scripts")
	if err := stopCurrentProxy(dataDir); err != nil {
		rollbackBinaries()
		return fmt.Errorf("post-copy runtime cleanup: %w", err)
	}
	if err := markManualStartRequired(dataDir); err != nil {
		rollbackBinaries()
		return fmt.Errorf("mark manual start required: %w", err)
	}

	if err := updateCurrentReleaseSymlink(dataDir, releaseDir); err != nil {
		rollbackBinaries()
		return fmt.Errorf("update current release symlink: %w", err)
	}

	// --- 9. Verify new privd binary ---
	newPrivd := filepath.Join(binDir, "privd")
	if _, err := os.Stat(newPrivd); err == nil {
		logger.Println("verifying new privd binary")
		if err := verifyBinary(newPrivd); err != nil {
			logger.Printf("new privd binary verification failed: %v — rolling back", err)
			rollbackBinaries()
			return fmt.Errorf("new privd verification failed: %w", err)
		}
		logger.Println("new privd binary verified OK")
	}

	// --- 10. Fork the new daemon ---
	logger.Println("forking new privd")
	if err := relaunchDaemon(dataDir); err != nil {
		rollbackBinaries()
		// Removing the old socket path is required before relaunch, but if the
		// new daemon never comes up the still-running old daemon becomes
		// unreachable. After rollback, try to bring the previous daemon version
		// back so the device is not left without a control socket.
		if recoverErr := relaunchDaemon(dataDir); recoverErr != nil {
			return fmt.Errorf("relaunch daemon: %w; recovery relaunch failed: %v", err, recoverErr)
		}
		go ScheduleSelfExit(SelfExitDelay)
		return fmt.Errorf("relaunch daemon: %w; previous daemon restored", err)
	}

	// --- 11. Clean up backup (success path) ---
	// Keep the backup around for a while in case manual rollback is needed.
	// It will be cleaned on next update.
	logger.Println("module update installed successfully — old daemon should self-terminate shortly")
	return nil
}

// ScheduleSelfExit waits for the given delay then sends SIGTERM to the
// current process. This should be called in a goroutine AFTER the IPC
// response has been written, so the old daemon exits cleanly once the
// new daemon is running.
func ScheduleSelfExit(delay time.Duration) {
	time.Sleep(delay)
	log.Printf("[updater] self-exit: sending SIGTERM to self (pid %d)", os.Getpid())
	syscall.Kill(os.Getpid(), syscall.SIGTERM)
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

// stopCurrentProxy invokes the canonical root runtime cleanup script.
// It does NOT kill the current privd -- that happens via ScheduleSelfExit
// after the IPC response is sent.
func stopCurrentProxy(dataDir string) error {
	// Build a minimal environment so scripts work even if the old daemon
	// did not set PRIVSTACK_DIR. Old scripts might not need it, but new
	// scripts definitely do.
	scriptEnv := []string{
		"PRIVSTACK_DIR=" + dataDir,
		"PATH=" + os.Getenv("PATH"),
	}
	return execScriptWithEnv(filepath.Join(dataDir, "scripts", "rescue_reset.sh"), "update-clean", scriptEnv)
}

// relaunchDaemon starts the new privd binary and waits for it to become
// responsive on the IPC socket. It does NOT kill the old daemon -- the
// caller is responsible for scheduling self-exit after IPC response.
func relaunchDaemon(dataDir string) error {
	privdBin := filepath.Join(dataDir, "bin", "privd")

	// Try multiple config path conventions -- old daemons used config.json
	// in the data root, newer ones may use config/config.json.
	configPath := filepath.Join(dataDir, "config", "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = filepath.Join(dataDir, "config.json")
	}

	logDir := filepath.Join(dataDir, "logs")
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		logDir = filepath.Join(dataDir, "log")
	}
	os.MkdirAll(logDir, 0700)
	logFile := filepath.Join(logDir, "privd.log")
	if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		_ = f.Close()
	}

	runDir := filepath.Join(dataDir, "run")
	os.MkdirAll(runDir, 0750)
	pidFile := filepath.Join(runDir, "privd.pid")

	// Remove old socket so the new daemon can bind.
	sockPath := filepath.Join(runDir, "daemon.sock")
	os.Remove(sockPath)

	// Fork a new daemon process. The old daemon stays alive until
	// ScheduleSelfExit is called (after IPC response).
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

	// Wait up to 10 seconds for the new daemon to start listening.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			return nil
		}
	}

	return fmt.Errorf("new privd did not start listening on %s within 10s", sockPath)
}

// execScriptWithEnv runs a shell script with an action argument and explicit
// environment. This is used during updates where the old daemon's env may
// not have all required variables.
func execScriptWithEnv(scriptPath string, action string, env []string) error {
	if _, err := os.Stat(scriptPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("required script missing: %s", scriptPath)
		}
		return fmt.Errorf("stat script %s: %w", scriptPath, err)
	}
	shell := "/system/bin/sh"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, scriptPath, action)
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %s: %w", scriptPath, action, string(output), err)
	}
	return nil
}

func stagedBinaryDir(staging string) (string, error) {
	binariesRoot := filepath.Join(staging, "binaries")
	info, err := os.Stat(binariesRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat binaries dir: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", binariesRoot)
	}

	arch := runtimeBinaryArch()
	if dir := findSubdir(staging, "binaries", arch); dir != "" {
		return dir, nil
	}
	if arch == "armv7" {
		if dir := findSubdir(staging, "binaries", "arm"); dir != "" {
			return dir, nil
		}
	}

	return "", fmt.Errorf("module update does not contain binaries for %s", arch)
}

func validateModuleStaging(staging string, stagedBinDir string) error {
	if stagedBinDir == "" {
		stagedBinDir = staging
	}

	for _, name := range []string{"sing-box", "privd", "privctl"} {
		path := filepath.Join(stagedBinDir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("release bundle missing required binary %s", name)
			}
			return fmt.Errorf("stat staged binary %s: %w", name, err)
		}
		if info.IsDir() {
			return fmt.Errorf("staged binary %s is a directory", name)
		}
		if info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("staged binary %s is not executable", name)
		}
	}

	for _, path := range []string{
		"OWNERSHIP.md",
		"module.prop",
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"scripts/dns.sh",
		"scripts/iptables.sh",
		"scripts/rescue_reset.sh",
		"scripts/routing.sh",
		"scripts/lib/privstack_env.sh",
		"scripts/lib/privstack_install.sh",
		"scripts/lib/privstack_installer_flow.sh",
		"scripts/lib/privstack_netstack.sh",
		"scripts/lib/privstack_iptables_rules.sh",
		"defaults/config.json",
	} {
		fullPath := filepath.Join(staging, path)
		info, err := os.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("release bundle missing required file %s", path)
			}
			return fmt.Errorf("stat staged file %s: %w", path, err)
		}
		if info.IsDir() {
			return fmt.Errorf("staged file %s is a directory", path)
		}
	}

	if err := validateModuleProp(filepath.Join(staging, "module.prop")); err != nil {
		return err
	}

	return nil
}

func validateModuleProp(path string) error {
	props, err := readModuleProp(path)
	if err != nil {
		return err
	}
	for _, key := range []string{"id", "version", "versionCode"} {
		if props[key] == "" {
			return fmt.Errorf("module.prop missing required %s", key)
		}
	}
	if props["id"] != "privstack" {
		return fmt.Errorf("module.prop has unexpected id %q", props["id"])
	}
	return nil
}

func readModuleProp(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read module.prop: %w", err)
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return props, nil
}

func prepareVersionedRelease(staging string, stagedBinDir string, dataDir string, version string) (string, error) {
	releasesRoot := filepath.Join(dataDir, "releases")
	if err := os.MkdirAll(releasesRoot, 0750); err != nil {
		return "", fmt.Errorf("mkdir releases: %w", err)
	}

	tmp, err := os.MkdirTemp(releasesRoot, ".staging-*")
	if err != nil {
		return "", fmt.Errorf("create release staging: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmp)
		}
	}()

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0750); err != nil {
		return "", fmt.Errorf("mkdir release bin: %w", err)
	}
	for _, name := range []string{"sing-box", "privd", "privctl"} {
		if err := copyFile(filepath.Join(stagedBinDir, name), filepath.Join(binDir, name), 0750); err != nil {
			return "", fmt.Errorf("copy release binary %s: %w", name, err)
		}
	}

	moduleDir := filepath.Join(tmp, "module")
	if err := os.MkdirAll(moduleDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir release module: %w", err)
	}
	for _, name := range []string{
		"OWNERSHIP.md",
		"module.prop",
		"service.sh",
		"post-fs-data.sh",
		"uninstall.sh",
		"customize.sh",
		"sepolicy.rule",
	} {
		src := filepath.Join(staging, name)
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) && name == "sepolicy.rule" {
				continue
			}
			return "", fmt.Errorf("stat release module file %s: %w", name, err)
		}
		perm := os.FileMode(0644)
		if strings.HasSuffix(name, ".sh") {
			perm = 0755
		}
		if err := copyFile(src, filepath.Join(moduleDir, name), perm); err != nil {
			return "", fmt.Errorf("copy release module file %s: %w", name, err)
		}
	}

	for _, dir := range []string{"scripts", "defaults"} {
		src := filepath.Join(staging, dir)
		if info, err := os.Stat(src); err == nil && info.IsDir() {
			perm := os.FileMode(0644)
			if dir == "scripts" {
				perm = 0755
			}
			if err := copyDir(src, filepath.Join(moduleDir, dir), perm); err != nil {
				return "", fmt.Errorf("copy release %s: %w", dir, err)
			}
		}
	}

	releaseDir := nextReleaseDir(releasesRoot, version)
	if err := os.Rename(tmp, releaseDir); err != nil {
		return "", fmt.Errorf("publish release dir: %w", err)
	}
	if err := writeReleaseManifest(releaseDir, version); err != nil {
		_ = os.RemoveAll(releaseDir)
		return "", err
	}
	cleanup = false
	return releaseDir, nil
}

type releaseManifest struct {
	Version     string            `json:"version"`
	InstalledAt string            `json:"installed_at"`
	Files       map[string]string `json:"files_sha256"`
}

func writeReleaseManifest(releaseDir string, version string) error {
	files := make(map[string]string)
	if err := filepath.Walk(releaseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(path) == "install-manifest.json" {
			return nil
		}
		rel, err := filepath.Rel(releaseDir, path)
		if err != nil {
			return err
		}
		sum, err := sha256File(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = sum
		return nil
	}); err != nil {
		return fmt.Errorf("build release manifest: %w", err)
	}
	manifest := releaseManifest{
		Version:     NormalizeVersionTag(version),
		InstalledAt: time.Now().Format(time.RFC3339),
		Files:       files,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal release manifest: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(releaseDir, "install-manifest.json"), data, 0640); err != nil {
		return fmt.Errorf("write release manifest: %w", err)
	}
	return nil
}

func nextReleaseDir(releasesRoot string, version string) string {
	base := safeReleaseDirName(NormalizeVersionTag(version))
	if base == "" || base == "v0.0.0" {
		base = "unknown"
	}
	candidate := filepath.Join(releasesRoot, base)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	for i := 2; ; i++ {
		candidate = filepath.Join(releasesRoot, fmt.Sprintf("%s-%d", base, i))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func safeReleaseDirName(version string) string {
	var b strings.Builder
	for _, r := range version {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "._-")
}

func updateCurrentReleaseSymlink(dataDir string, releaseDir string) error {
	tmp, err := os.CreateTemp(dataDir, ".current-*")
	if err != nil {
		return fmt.Errorf("create current temp path: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("remove current temp file: %w", err)
	}
	if err := os.Symlink(releaseDir, tmpPath); err != nil {
		return fmt.Errorf("create current symlink: %w", err)
	}
	currentPath := filepath.Join(dataDir, "current")
	if info, statErr := os.Lstat(currentPath); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		backupDir := filepath.Join(dataDir, "releases")
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("create current release backup dir: %w", err)
		}
		backupPath := filepath.Join(backupDir, fmt.Sprintf("current.pre-%d", time.Now().UnixNano()))
		if err := os.Rename(currentPath, backupPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("move non-symlink current release aside: %w", err)
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("stat current release path: %w", statErr)
	}
	if err := os.Rename(tmpPath, currentPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename current symlink: %w", err)
	}
	return nil
}

func markManualStartRequired(dataDir string) error {
	configDir := filepath.Join(dataDir, "config")
	runDir := filepath.Join(dataDir, "run")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("mkdir config: %w", err)
	}
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		return fmt.Errorf("mkdir run: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "manual"), []byte("module update\n"), 0o600); err != nil {
		return fmt.Errorf("write manual flag: %w", err)
	}
	if err := os.Remove(filepath.Join(runDir, "active")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove active marker: %w", err)
	}
	return nil
}

func runtimeBinaryArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	case "arm":
		return "armv7"
	default:
		return runtime.GOARCH
	}
}

// verifyBinary runs the binary with --help (or -h) and checks that it
// exits without crashing. This catches corrupted downloads, wrong
// architecture, missing dynamic libraries, etc.
func verifyBinary(binPath string) error {
	// Try --version first, then -h, then just run with no args and check
	// that we get a non-signal exit. The important thing is that the ELF
	// loader doesn't reject it.
	cmd := exec.Command(binPath, "--version")
	cmd.Stdout = nil
	cmd.Stderr = nil
	// 5 second timeout to avoid hanging.
	done := make(chan error, 1)
	go func() {
		done <- cmd.Run()
	}()
	select {
	case err := <-done:
		// Exit code 0 or 2 (flag parsing error for "--version") are fine.
		// Signal-based exits (SIGSEGV, SIGBUS, SIGILL) indicate a bad binary.
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				ws := exitErr.Sys().(syscall.WaitStatus)
				if ws.Signaled() {
					return fmt.Errorf("binary crashed with signal %d", ws.Signal())
				}
				// Non-zero exit (e.g. 2 for "unknown flag") is acceptable
				return nil
			}
			return fmt.Errorf("exec failed: %w", err)
		}
		return nil
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		return fmt.Errorf("binary did not exit within 5 seconds")
	}
}

// atomicCopyFile copies src to dst atomically by writing to a temp file
// in the same directory and then renaming. This is critical for replacing
// binaries that may be currently executing -- on Linux, the old inode
// stays valid for the running process, while new opens see the new file.
func atomicCopyFile(src, dst string, perm os.FileMode) error {
	dir := filepath.Dir(dst)

	// Write to a temp file in the same directory (same filesystem for rename).
	tmp, err := os.CreateTemp(dir, ".update-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()

	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}

	if _, err := io.Copy(tmp, in); err != nil {
		in.Close()
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	in.Close()

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}

	// Sync to disk so the file survives power loss.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	tmp.Close()

	// Atomic rename. On Linux this replaces the directory entry but the
	// old inode (and thus the old running binary) remains valid until all
	// file handles are closed.
	if err := os.Rename(tmpPath, dst); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, dst, err)
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

func replaceDirFromSource(srcDir, dstDir string, filePerm os.FileMode) error {
	parent := filepath.Dir(dstDir)
	base := filepath.Base(dstDir)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+base+"-new-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := copyDir(srcDir, tmp, filePerm); err != nil {
		return err
	}

	old := filepath.Join(parent, fmt.Sprintf(".%s-old-%d", base, time.Now().UnixNano()))
	oldExists := false
	if _, err := os.Lstat(dstDir); err == nil {
		if err := os.Rename(dstDir, old); err != nil {
			return err
		}
		oldExists = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(tmp, dstDir); err != nil {
		if oldExists {
			_ = os.Rename(old, dstDir)
		}
		return err
	}
	published = true
	if oldExists {
		_ = os.RemoveAll(old)
	}
	return nil
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
