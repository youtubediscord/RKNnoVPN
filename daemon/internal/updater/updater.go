// Package updater checks GitHub Releases for new versions, downloads assets,
// and verifies SHA256 checksums. The daemon has full network access (runs as
// root), so all HTTP requests happen here -- the APK panel has NO INTERNET
// permission and delegates everything through IPC.
package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	releasesURL = "https://api.github.com/repos/youtubediscord/RKNnoVPN/releases/latest"
	httpTimeout = 30 * time.Second
)

// --------------------------------------------------------------------------
// Public types
// --------------------------------------------------------------------------

// UpdateInfo describes the result of a version check.
type UpdateInfo struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	HasUpdate      bool   `json:"has_update"`
	Changelog      string `json:"changelog"`
	ModuleURL      string `json:"module_url"`
	ApkURL         string `json:"apk_url"`
	ChecksumURL    string `json:"checksum_url"`
	ModuleSize     int64  `json:"module_size"`
	ApkSize        int64  `json:"apk_size"`
}

// DownloadedUpdate holds paths to verified downloaded assets.
type DownloadedUpdate struct {
	ModulePath string `json:"module_path"` // path to downloaded module.zip
	ApkPath    string `json:"apk_path"`    // path to downloaded panel.apk
	Checksums  bool   `json:"checksums"`   // SHA256 verified
}

// --------------------------------------------------------------------------
// GitHub release JSON schema (subset)
// --------------------------------------------------------------------------

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Body    string    `json:"body"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// --------------------------------------------------------------------------
// CheckForUpdate
// --------------------------------------------------------------------------

// CheckForUpdate queries the GitHub Releases API and compares the latest
// tag against currentVersion. Both are expected in "vX.Y.Z" format.
func CheckForUpdate(currentVersion string) (*UpdateInfo, error) {
	client := &http.Client{Timeout: httpTimeout}
	currentVersion = NormalizeVersionTag(currentVersion)

	req, err := http.NewRequest("GET", releasesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("updater: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "RKNnoVPN-updater/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("updater: fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("updater: github returned %d", resp.StatusCode)
	}

	var release ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("updater: decode release: %w", err)
	}

	info := &UpdateInfo{
		CurrentVersion: currentVersion,
		LatestVersion:  release.TagName,
		Changelog:      release.Body,
	}

	// Locate assets.
	for _, a := range release.Assets {
		nameLower := strings.ToLower(a.Name)
		switch {
		case strings.Contains(nameLower, "module") && strings.HasSuffix(nameLower, ".zip"):
			info.ModuleURL = a.BrowserDownloadURL
			info.ModuleSize = a.Size
		case strings.Contains(nameLower, "panel") && strings.HasSuffix(nameLower, ".apk"):
			info.ApkURL = a.BrowserDownloadURL
			info.ApkSize = a.Size
		case nameLower == "sha256sums.txt":
			info.ChecksumURL = a.BrowserDownloadURL
		}
	}

	info.HasUpdate = compareSemver(currentVersion, release.TagName)
	return info, nil
}

func NormalizeVersionTag(version string) string {
	trimmed := strings.TrimSpace(version)
	trimmed = strings.TrimLeft(trimmed, "vV")
	if trimmed == "" {
		return "v0.0.0"
	}
	return "v" + trimmed
}

// --------------------------------------------------------------------------
// DownloadUpdate
// --------------------------------------------------------------------------

// DownloadUpdate downloads module.zip and panel.apk into destDir and verifies
// SHA256 checksums before the artifacts can be installed. The progress callback
// is called periodically with downloaded/total byte counts.
func DownloadUpdate(info *UpdateInfo, destDir string, progress func(downloaded, total int64)) (*DownloadedUpdate, error) {
	if err := os.MkdirAll(destDir, 0750); err != nil {
		return nil, fmt.Errorf("updater: mkdir %s: %w", destDir, err)
	}

	totalSize := info.ModuleSize + info.ApkSize
	var downloaded int64

	report := func(n int64) {
		downloaded += n
		if progress != nil {
			progress(downloaded, totalSize)
		}
	}

	result := &DownloadedUpdate{}

	// Download module zip.
	if info.ModuleURL != "" {
		p := filepath.Join(destDir, "module.zip")
		if err := downloadFile(info.ModuleURL, p, report); err != nil {
			return nil, fmt.Errorf("updater: download module: %w", err)
		}
		result.ModulePath = p
	}

	// Download APK.
	if info.ApkURL != "" {
		p := filepath.Join(destDir, "panel.apk")
		if err := downloadFile(info.ApkURL, p, report); err != nil {
			return nil, fmt.Errorf("updater: download apk: %w", err)
		}
		result.ApkPath = p
	}

	if result.ModulePath == "" && result.ApkPath == "" {
		return nil, fmt.Errorf("updater: no downloadable update artifacts found")
	}
	if info.ChecksumURL == "" {
		return nil, fmt.Errorf("updater: release is missing sha256sums.txt")
	}

	checksumPath := filepath.Join(destDir, "SHA256SUMS.txt")
	if err := downloadFile(info.ChecksumURL, checksumPath, nil); err != nil {
		return nil, fmt.Errorf("updater: download checksums: %w", err)
	}

	ok, err := verifyChecksums(checksumPath, destDir)
	if err != nil {
		return nil, fmt.Errorf("updater: verify checksums: %w", err)
	}
	result.Checksums = ok
	if !ok {
		return nil, fmt.Errorf("updater: SHA256 checksum mismatch")
	}

	return result, nil
}

// VerifyDownloadedUpdate verifies that the artifacts about to be installed are
// the checksum-validated files produced by DownloadUpdate.
func VerifyDownloadedUpdate(modulePath, apkPath string) error {
	if modulePath == "" || apkPath == "" {
		return fmt.Errorf("both module and APK paths are required")
	}
	moduleDir := filepath.Dir(modulePath)
	apkDir := filepath.Dir(apkPath)
	if moduleDir != apkDir {
		return fmt.Errorf("module and APK must be in the same verified update directory")
	}
	if filepath.Base(modulePath) != "module.zip" || filepath.Base(apkPath) != "panel.apk" {
		return fmt.Errorf("update artifacts must be the verified module.zip and panel.apk files")
	}
	if _, err := os.Stat(modulePath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("missing module.zip")
		}
		return fmt.Errorf("stat module.zip: %w", err)
	}
	if _, err := os.Stat(apkPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("missing panel.apk")
		}
		return fmt.Errorf("stat panel.apk: %w", err)
	}
	checksumPath := filepath.Join(moduleDir, "SHA256SUMS.txt")
	if _, err := os.Stat(checksumPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("missing SHA256SUMS.txt for downloaded update")
		}
		return fmt.Errorf("stat SHA256SUMS.txt: %w", err)
	}
	ok, err := verifyChecksums(checksumPath, moduleDir)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("SHA256 checksum mismatch")
	}
	return nil
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// downloadFile fetches a URL to a local path, calling onProgress with each
// chunk's byte count.
func downloadFile(url, dest string, onProgress func(int64)) error {
	client := &http.Client{Timeout: 10 * time.Minute}

	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 64*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if onProgress != nil {
				onProgress(int64(n))
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

// verifyChecksums reads SHA256SUMS.txt and verifies every downloaded update
// artifact in dir against an explicit recorded hash. Missing checksum entries
// are failures; otherwise the UI could report unverified artifacts as verified.
func verifyChecksums(sumFile, dir string) (bool, error) {
	data, err := os.ReadFile(sumFile)
	if err != nil {
		return false, err
	}

	required := map[string]bool{}
	for _, name := range []string{"module.zip", "panel.apk"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			required[name] = false
		} else if err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("stat %s: %w", name, err)
		}
	}
	if len(required) == 0 {
		return false, fmt.Errorf("no downloaded update artifacts to verify")
	}

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Format: "<hex>  <filename>" or "<hex> <filename>"
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		expectedHash := strings.TrimSpace(parts[0])
		if len(expectedHash) != sha256.Size*2 {
			continue
		}
		if _, err := hex.DecodeString(expectedHash); err != nil {
			continue
		}
		fileName := filepath.Base(strings.TrimLeft(parts[1], " *"))

		// Map release asset names to our local filenames.
		localName := ""
		fileNameLower := strings.ToLower(fileName)
		switch {
		case strings.Contains(fileNameLower, "module") && strings.HasSuffix(fileNameLower, ".zip"):
			localName = "module.zip"
		case strings.Contains(fileNameLower, "panel") && strings.HasSuffix(fileNameLower, ".apk"):
			localName = "panel.apk"
		default:
			continue // skip files we didn't download
		}

		localPath := filepath.Join(dir, localName)
		if _, err := os.Stat(localPath); os.IsNotExist(err) {
			continue
		}

		if matched, ok := required[localName]; ok && matched {
			return false, fmt.Errorf("%s: duplicate checksum entry", localName)
		}

		actualHash, err := sha256File(localPath)
		if err != nil {
			return false, fmt.Errorf("hash %s: %w", localName, err)
		}

		if !strings.EqualFold(actualHash, expectedHash) {
			return false, fmt.Errorf("%s: expected %s, got %s", localName, expectedHash, actualHash)
		}
		if _, ok := required[localName]; ok {
			required[localName] = true
		}
	}

	var missing []string
	for name, matched := range required {
		if !matched {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return false, fmt.Errorf("missing checksum entries for %s", strings.Join(missing, ", "))
	}

	return true, nil
}

// sha256File returns the hex-encoded SHA256 digest of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// compareSemver returns true when latest is strictly newer than current.
// Both inputs should start with "v" (e.g. "v1.2.3").
func compareSemver(current, latest string) bool {
	cur := parseSemver(NormalizeVersionTag(current))
	lat := parseSemver(NormalizeVersionTag(latest))

	for i := 0; i < 3; i++ {
		if lat[i] > cur[i] {
			return true
		}
		if lat[i] < cur[i] {
			return false
		}
	}
	return false
}

// parseSemver extracts [major, minor, patch] from a "vX.Y.Z" string.
// Missing or unparseable components default to 0.
func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	// Strip pre-release suffix (e.g. "-beta.1").
	if idx := strings.IndexByte(v, '-'); idx >= 0 {
		v = v[:idx]
	}
	parts := strings.SplitN(v, ".", 3)
	var result [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		result[i] = n
	}
	return result
}
