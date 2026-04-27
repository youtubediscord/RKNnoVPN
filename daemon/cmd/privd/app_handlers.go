package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/youtubediscord/RKNnoVPN/daemon/internal/ipc"
)

func (d *daemon) handleAppList(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	apps, err := loadInstalledApps()
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "load apps failed: " + err.Error(),
		}
	}
	return apps, nil
}

func (d *daemon) handleResolveUID(params *json.RawMessage) (interface{}, *ipc.RPCError) {
	if params == nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "params required: {\"uid\": 12345}",
		}
	}

	var p struct {
		UID int `json:"uid"`
	}
	if err := json.Unmarshal(*params, &p); err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "invalid params: " + err.Error(),
		}
	}
	if p.UID <= 0 {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInvalidParams,
			Message: "uid must be > 0",
		}
	}

	apps, err := loadInstalledApps()
	if err != nil {
		return nil, &ipc.RPCError{
			Code:    ipc.CodeInternalError,
			Message: "load apps failed: " + err.Error(),
		}
	}

	var fallback *daemonAppInfo
	for _, app := range apps {
		if app.UID == p.UID {
			return app, nil
		}
		if fallback == nil && app.UID%100000 == p.UID%100000 {
			appCopy := app
			fallback = &appCopy
		}
	}
	if fallback != nil {
		return *fallback, nil
	}

	return nil, &ipc.RPCError{
		Code:    ipc.CodeInvalidParams,
		Message: fmt.Sprintf("no package found for uid %d", p.UID),
	}
}

type daemonAppInfo struct {
	PackageName string  `json:"packageName"`
	AppName     string  `json:"appName"`
	UID         int     `json:"uid"`
	IsSystemApp bool    `json:"isSystemApp"`
	Category    string  `json:"category"`
	ApkPath     *string `json:"apkPath,omitempty"`
	VersionName *string `json:"versionName,omitempty"`
	Enabled     bool    `json:"enabled"`
}

func loadInstalledApps() ([]daemonAppInfo, error) {
	data, err := os.ReadFile("/data/system/packages.list")
	if err != nil {
		return nil, fmt.Errorf("read packages.list: %w", err)
	}

	apps := make([]daemonAppInfo, 0)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		uid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		dataDir := ""
		if len(fields) >= 4 {
			dataDir = fields[3]
		}

		appName := prettyPackageLabel(fields[0])

		isSystem := strings.HasPrefix(dataDir, "/system/") ||
			strings.HasPrefix(dataDir, "/vendor/") ||
			strings.HasPrefix(dataDir, "/product/") ||
			strings.HasPrefix(dataDir, "/system_ext/")

		category := classifyDaemonApp(fields[0], isSystem)

		apps = append(apps, daemonAppInfo{
			PackageName: fields[0],
			AppName:     appName,
			UID:         uid,
			IsSystemApp: isSystem,
			Category:    category,
			Enabled:     true,
		})
	}

	return apps, nil
}

func prettyPackageLabel(packageName string) string {
	last := packageName
	if idx := strings.LastIndex(packageName, "."); idx != -1 && idx+1 < len(packageName) {
		last = packageName[idx+1:]
	}
	last = strings.ReplaceAll(last, "_", " ")
	last = strings.ReplaceAll(last, "-", " ")
	if last == "" {
		return packageName
	}
	return strings.ToUpper(last[:1]) + last[1:]
}

func classifyDaemonApp(packageName string, isSystem bool) string {
	if isSystem {
		return "SYSTEM"
	}

	lower := strings.ToLower(packageName)
	switch {
	case strings.Contains(lower, "telegram"),
		strings.Contains(lower, "whatsapp"),
		strings.Contains(lower, "discord"),
		strings.Contains(lower, "signal"),
		strings.Contains(lower, "messenger"):
		return "MESSAGING"
	case strings.Contains(lower, "youtube"),
		strings.Contains(lower, "netflix"),
		strings.Contains(lower, "twitch"),
		strings.Contains(lower, "video"):
		return "VIDEO"
	case strings.Contains(lower, "spotify"),
		strings.Contains(lower, "music"),
		strings.Contains(lower, "audio"):
		return "AUDIO"
	case strings.Contains(lower, "chrome"),
		strings.Contains(lower, "firefox"),
		strings.Contains(lower, "browser"),
		strings.Contains(lower, "opera"),
		strings.Contains(lower, "brave"):
		return "BROWSER"
	case strings.Contains(lower, "game"):
		return "GAME"
	case strings.Contains(lower, "bank"),
		strings.Contains(lower, "wallet"),
		strings.Contains(lower, "finance"),
		strings.Contains(lower, "sber"),
		strings.Contains(lower, "tinkoff"):
		return "PRODUCTIVITY"
	case strings.Contains(lower, "social"),
		strings.Contains(lower, "twitter"),
		strings.Contains(lower, "instagram"),
		strings.Contains(lower, "reddit"),
		strings.Contains(lower, "facebook"),
		strings.Contains(lower, "vk"):
		return "SOCIAL"
	default:
		return "OTHER"
	}
}
