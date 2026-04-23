package core

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const networkStackUID = "1073"

var builtInAlwaysDirectExact = map[string]bool{
	// Sensitive Russian apps that should never be routed through PrivStack.
	"ru.oneme.app":                   true, // MAX
	"ru.vtb24.mobilebanking.android": true,
	"com.avito.android":              true,
	"ru.ozon.app.android":            true,
	"com.wildberries.ru":             true,
	"ru.beru.android":                true,
	"ru.yandex.taxi":                 true,
	"ru.yandex.yandexmaps":           true,
	"ru.yandex.searchplugin":         true,
	"ru.yandex.browser":              true,
	"ru.yandex.browser.lite":         true,
	"ru.yandex.music":                true,
	"ru.yandex.disk":                 true,
	"ru.yandex.mail":                 true,
	"ru.yandex.market":               true,
	"ru.yandex.metro":                true,
	"ru.yandex.weatherplugin":        true,
	"ru.yandex.mobile.auth":          true,
	"ru.sberbankmobile":              true,
	"ru.sberbankmobile.arm":          true,
	"ru.alfabank.mobile.android":     true,
	"ru.tinkoff.android":             true,
	"ru.tinkoff.investing":           true,
	"com.idamob.tinkoff.android":     true,
	"ru.raiffeisennews":              true,
	"ru.rosbank.android":             true,
	"ru.psbank.online":               true,
	"ru.mts.bank":                    true,
	"ru.gosuslugi.pos":               true,
	"ru.fns.lkfl":                    true,
	"ru.nalog.ibr":                   true,
	"ru.mos.app":                     true,
	"ru.mts.mymts":                   true,
	"com.beeline.dc":                 true,
	"ru.megafon.mlk":                 true,
	"ru.tele2.mytele2":               true,

	// VPN/proxy clients and network cores.
	"com.wireguard.android":                true,
	"org.torproject.android":               true,
	"ch.protonvpn.android":                 true,
	"net.mullvad.mullvadvpn":               true,
	"com.cloudflare.onedotonedotonedotone": true,
	"org.amnezia.vpn":                      true,
	"app.hiddify.com":                      true,
	"com.v2ray.ang":                        true,
	"io.nekohasekai.sfa":                   true,
	"io.nekohasekai.sagernet":              true,
	"moe.nb4a":                             true,
	"org.outline.android.client":           true,
	"net.openvpn.openvpn":                  true,
	"de.blinkt.openvpn":                    true,
	"com.github.shadowsocks":               true,
	"com.getsurfboard":                     true,
	"com.github.kr328.clash":               true,
	"com.github.metacubex.clash.meta":      true,
}

var builtInAlwaysDirectPrefixes = []string{
	"ru.yandex.",
	"com.yandex.",
	"ru.vtb",
	"ru.sber",
	"ru.alfabank",
	"ru.tinkoff",
	"com.idamob.tinkoff",
	"ru.raiffeisen",
	"ru.rosbank",
	"ru.psbank",
	"ru.mts.bank",
	"ru.gosuslugi",
	"ru.fns",
	"ru.nalog",
	"ru.mos",
	"com.avito",
	"ru.ozon",
	"com.wildberries",
}

var builtInAlwaysDirectKeywords = []string{
	"vpn",
	"proxy",
	"v2ray",
	"xray",
	"hiddify",
	"nekobox",
	"nekoray",
	"amnezia",
	"wireguard",
	"outline",
	"openvpn",
	"shadowsocks",
	"clash",
	"singbox",
	"sing-box",
	"sagernet",
	"tun2socks",
}

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
		pkg = strings.TrimSpace(pkg)
		if pkg != "" {
			wanted[pkg] = true
		}
	}
	if len(wanted) == 0 {
		return ""
	}

	return resolvePackageUIDsMatching(func(pkgName string) bool {
		return wanted[pkgName]
	})
}

// ResolveAlwaysDirectUIDs resolves user-configured and built-in packages that
// must bypass PrivStack before TPROXY/DNS interception.
func ResolveAlwaysDirectUIDs(packages []string) string {
	userPackages := make(map[string]bool, len(packages))
	for _, pkg := range packages {
		pkg = strings.TrimSpace(pkg)
		if pkg != "" {
			userPackages[pkg] = true
		}
	}

	return resolvePackageUIDsMatching(func(pkgName string) bool {
		return userPackages[pkgName] || IsBuiltInAlwaysDirectPackage(pkgName)
	})
}

// BuildBypassUIDs returns the complete UID bypass list used by iptables/DNS.
func BuildBypassUIDs(alwaysDirectPackages []string) string {
	return joinUniqueFields(networkStackUID, ResolveAlwaysDirectUIDs(alwaysDirectPackages))
}

// AppRoutingEnv is the explicit UID/scope contract passed to the shell
// firewall and DNS scripts. APP_UIDS is kept only as a legacy mirror.
type AppRoutingEnv struct {
	AppMode      string
	AppUIDs      string
	ProxyUIDs    string
	DirectUIDs   string
	BypassUIDs   string
	DNSScope     string
	LegacyDNSMode string
}

// BuildAppRoutingEnv resolves package names into unambiguous UID sets for
// proxy, direct and hard-bypass traffic.
func BuildAppRoutingEnv(mode string, packages []string, alwaysDirectPackages []string) AppRoutingEnv {
	appMode := MapAppMode(mode)
	env := AppRoutingEnv{
		AppMode:    appMode,
		BypassUIDs: BuildBypassUIDs(alwaysDirectPackages),
	}

	selectedUIDs := ResolvePackageUIDs(packages)
	switch appMode {
	case "whitelist":
		env.ProxyUIDs = selectedUIDs
		env.AppUIDs = selectedUIDs
		env.DNSScope = "uids"
		env.LegacyDNSMode = "per_uid"
	case "blacklist":
		env.DirectUIDs = selectedUIDs
		env.AppUIDs = selectedUIDs
		env.DNSScope = "all_except_uids"
		env.LegacyDNSMode = "per_uid"
	case "off":
		env.DNSScope = "off"
		env.LegacyDNSMode = "off"
	default:
		env.AppMode = "all"
		env.DNSScope = "all"
		env.LegacyDNSMode = "all"
	}

	return env
}

// IsBuiltInAlwaysDirectPackage reports whether a package is part of the
// built-in hard-direct policy for sensitive apps and network clients.
func IsBuiltInAlwaysDirectPackage(pkgName string) bool {
	if builtInAlwaysDirectExact[pkgName] {
		return true
	}
	for _, prefix := range builtInAlwaysDirectPrefixes {
		if strings.HasPrefix(pkgName, prefix) {
			return true
		}
	}
	lower := strings.ToLower(pkgName)
	for _, keyword := range builtInAlwaysDirectKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func resolvePackageUIDsMatching(match func(string) bool) string {
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
		if !match(pkgName) {
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

	sort.Slice(uids, func(i, j int) bool {
		left, leftErr := strconv.Atoi(uids[i])
		right, rightErr := strconv.Atoi(uids[j])
		if leftErr != nil || rightErr != nil {
			return uids[i] < uids[j]
		}
		return left < right
	})

	return strings.Join(uids, " ")
}

func joinUniqueFields(values ...string) string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		for _, field := range strings.Fields(value) {
			if !seen[field] {
				seen[field] = true
				result = append(result, field)
			}
		}
	}
	return strings.Join(result, " ")
}

// MapAppMode converts config apps.mode values to the shell-script APP_MODE
// values expected by iptables.sh.
func MapAppMode(mode string) string {
	switch mode {
	case "whitelist", "include":
		return "whitelist"
	case "blacklist", "exclude":
		return "blacklist"
	case "off", "direct", "disabled":
		return "off"
	case "all":
		return "all"
	default:
		return "all"
	}
}
