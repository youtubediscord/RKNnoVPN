package diagnostics

import (
	"os"
	"strings"
)

func ReadModuleVersion(paths ...string) map[string]string {
	if len(paths) == 0 {
		paths = []string{
			"/data/adb/modules/rknnovpn/module.prop",
			"/data/adb/modules_update/rknnovpn/module.prop",
		}
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		result := map[string]string{"path": path}
		for _, line := range SplitLines(string(data)) {
			key, value, ok := strings.Cut(line, "=")
			if ok && (key == "version" || key == "versionCode") {
				result[key] = value
			}
		}
		return result
	}
	return map[string]string{"version": "unknown"}
}
