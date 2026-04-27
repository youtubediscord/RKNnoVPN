package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const profileFileName = "profile.json"

func Path(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), profileFileName)
}

func Load(path string) (Document, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Document{}, false, nil
		}
		return Document{}, false, fmt.Errorf("profile: read %s: %w", path, err)
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Document{}, false, fmt.Errorf("profile: parse %s: %w", path, err)
	}
	normalized, _, err := Normalize(doc)
	if err != nil {
		return Document{}, false, fmt.Errorf("profile: validate %s: %w", path, err)
	}
	return normalized, true, nil
}

func Save(path string, doc Document) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("profile: mkdir: %w", err)
	}
	normalized, _, err := Normalize(doc)
	if err != nil {
		return fmt.Errorf("profile: validate: %w", err)
	}
	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("profile: marshal: %w", err)
	}
	data = append(data, '\n')
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("profile: open %s: %w", tmpPath, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("profile: write %s: %w", tmpPath, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("profile: sync %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("profile: close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("profile: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("profile: rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}
