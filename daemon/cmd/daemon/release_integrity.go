package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

func diagnosticReleaseIntegrityIssues(report diagnosticReleaseIntegrity) []string {
	if report.MissingCurrent {
		return nil
	}
	issues := make([]string, 0)
	if report.Error != "" {
		issues = append(issues, "release integrity check failed: "+report.Error)
	}
	if report.MissingManifest && !report.OK {
		issues = append(issues, "current release manifest is missing")
	}
	if len(report.MissingFiles) > 0 {
		issues = append(issues, fmt.Sprintf("current release has %d missing file(s)", len(report.MissingFiles)))
	}
	if len(report.Mismatches) > 0 {
		issues = append(issues, fmt.Sprintf("current release has %d checksum mismatch(es)", len(report.Mismatches)))
	}
	return issues
}

func diagnosticReleaseIntegrityReport(dataDir string) diagnosticReleaseIntegrity {
	currentPath := filepath.Join(dataDir, "current")
	report := diagnosticReleaseIntegrity{
		CurrentPath: currentPath,
	}

	releasePath, err := filepath.EvalSymlinks(currentPath)
	if err != nil {
		if os.IsNotExist(err) {
			report.MissingCurrent = true
			report.OK = true
			return report
		}
		report.Error = err.Error()
		return report
	}
	report.ReleasePath = releasePath
	manifestPath := filepath.Join(releasePath, "install-manifest.json")
	report.ManifestPath = manifestPath
	report.Version = filepath.Base(releasePath)

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			report.MissingManifest = true
		} else {
			report.Error = err.Error()
		}
		return report
	}
	var manifest diagnosticReleaseManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		report.Error = err.Error()
		return report
	}
	report.Version = manifest.Version
	report.InstalledAt = manifest.InstalledAt
	if len(manifest.Files) == 0 {
		report.Error = "manifest contains no files"
		return report
	}

	paths := make([]string, 0, len(manifest.Files))
	for rel := range manifest.Files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		expected := strings.TrimSpace(manifest.Files[rel])
		if expected == "" {
			report.Mismatches = append(report.Mismatches, rel+": empty manifest hash")
			continue
		}
		fullPath := filepath.Join(releasePath, filepath.FromSlash(rel))
		actual, err := diagnosticSHA256File(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				report.MissingFiles = append(report.MissingFiles, rel)
			} else {
				report.Mismatches = append(report.Mismatches, rel+": "+err.Error())
			}
			continue
		}
		report.CheckedFiles++
		if !strings.EqualFold(actual, expected) {
			report.Mismatches = append(report.Mismatches, rel)
		}
	}
	report.OK = report.Error == "" && !report.MissingManifest && len(report.MissingFiles) == 0 && len(report.Mismatches) == 0
	return report
}

func diagnosticSHA256File(path string) (string, error) {
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

func supportedCapabilities() []string {
	return ipc.SupportedCapabilities()
}

func supportedRPCMethods() []string {
	return ipc.SupportedMethods()
}
