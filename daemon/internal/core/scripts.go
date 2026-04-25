package core

import (
	"context"
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

var (
	packageListPath          = "/data/system/packages.list"
	dataUserPath             = "/data/user"
	packageUIDCommandTimeout = 2 * time.Second
	runPackageUIDCommand     = defaultPackageUIDCommand
)

var SelfTestProtectedPackages = []string{
	"com.notcvnt.rknhardering",
	"com.yourvpndead",
}

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
	SelfTestProtectedPackages[0]:           true,
	SelfTestProtectedPackages[1]:           true,
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

// PackageUIDSourceStatus describes whether one Android package UID source can
// currently provide package -> UID mappings.
type PackageUIDSourceStatus struct {
	Source    string `json:"source"`
	Available bool   `json:"available"`
	Entries   int    `json:"entries,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PackageUIDResolution is the structured form behind the legacy
// space-separated UID resolver contract.
type PackageUIDResolution struct {
	Source             string                   `json:"source,omitempty"`
	UIDs               []string                 `json:"uids,omitempty"`
	UIDString          string                   `json:"uidString,omitempty"`
	RequestedPackages  []string                 `json:"requestedPackages,omitempty"`
	UnresolvedPackages []string                 `json:"unresolvedPackages,omitempty"`
	Errors             []string                 `json:"errors,omitempty"`
	Sources            []PackageUIDSourceStatus `json:"sources,omitempty"`
}

// PackageRoutingResolution reports both selected per-app routing packages and
// the hard-direct bypass package set from one shared source probe.
type PackageRoutingResolution struct {
	Selected     PackageUIDResolution     `json:"selected"`
	AlwaysDirect PackageUIDResolution     `json:"alwaysDirect"`
	Sources      []PackageUIDSourceStatus `json:"sources"`
	Errors       []string                 `json:"errors,omitempty"`
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
	return ResolvePackageUIDsDetailed(packages).UIDString
}

// ResolveAlwaysDirectUIDs resolves user-configured and built-in packages that
// must bypass PrivStack before TPROXY/DNS interception.
func ResolveAlwaysDirectUIDs(packages []string) string {
	return ResolveAlwaysDirectUIDsDetailed(packages).UIDString
}

// BuildBypassUIDs returns the complete UID bypass list used by iptables/DNS.
func BuildBypassUIDs(alwaysDirectPackages []string) string {
	return joinUniqueFields(networkStackUID, ResolveAlwaysDirectUIDs(alwaysDirectPackages))
}

// ResolvePackageUIDsDetailed resolves explicitly selected packages and keeps
// diagnostics about source selection and unresolved package names.
func ResolvePackageUIDsDetailed(packages []string) PackageUIDResolution {
	wanted := packageSet(packages)
	return resolvePackageUIDsFromSources(wanted.values(), func(pkgName string) bool {
		return wanted[pkgName]
	}, false)
}

// ResolveAlwaysDirectUIDsDetailed resolves user-configured and built-in
// packages that must bypass PrivStack, with structured diagnostics.
func ResolveAlwaysDirectUIDsDetailed(packages []string) PackageUIDResolution {
	userPackages := packageSet(packages)
	return resolvePackageUIDsFromSources(userPackages.values(), func(pkgName string) bool {
		return userPackages[pkgName] || IsBuiltInAlwaysDirectPackage(pkgName)
	}, false)
}

// BuildPackageRoutingResolution resolves both app-routing package sets from a
// shared source probe for doctor diagnostics.
func BuildPackageRoutingResolution(packages []string, alwaysDirectPackages []string) PackageRoutingResolution {
	catalogs := loadPackageUIDCatalogs(true)
	selectedWanted := packageSet(packages)
	alwaysWanted := packageSet(alwaysDirectPackages)
	selected := resolvePackageUIDsFromCatalogs(catalogs, selectedWanted.values(), func(pkgName string) bool {
		return selectedWanted[pkgName]
	})
	alwaysDirect := resolvePackageUIDsFromCatalogs(catalogs, alwaysWanted.values(), func(pkgName string) bool {
		return alwaysWanted[pkgName] || IsBuiltInAlwaysDirectPackage(pkgName)
	})
	return PackageRoutingResolution{
		Selected:     selected,
		AlwaysDirect: alwaysDirect,
		Sources:      sourceStatuses(catalogs),
		Errors:       sourceErrors(catalogs),
	}
}

// AppRoutingEnv is the explicit UID/scope contract passed to the shell
// firewall and DNS scripts. APP_UIDS is kept only as a legacy mirror.
type AppRoutingEnv struct {
	AppMode       string
	AppUIDs       string
	ProxyUIDs     string
	DirectUIDs    string
	BypassUIDs    string
	DNSScope      string
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

type normalizedPackageSet map[string]bool

type packageUIDCatalogResult struct {
	source  string
	uids    map[string]int
	status  PackageUIDSourceStatus
	errText string
}

func packageSet(packages []string) normalizedPackageSet {
	result := normalizedPackageSet{}
	for _, pkg := range packages {
		pkg = strings.TrimSpace(pkg)
		if pkg != "" {
			result[pkg] = true
		}
	}
	return result
}

func (s normalizedPackageSet) values() []string {
	values := make([]string, 0, len(s))
	for value := range s {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func resolvePackageUIDsFromSources(requested []string, match func(string) bool, probeAll bool) PackageUIDResolution {
	catalogs := make([]packageUIDCatalogResult, 0, 3)
	var best PackageUIDResolution
	bestResolvedRequested := -1
	bestUnresolved := len(requested) + 1

	for _, loader := range packageUIDCatalogLoaders() {
		catalog, err := loader.load()
		result := newPackageUIDCatalogResult(loader.source, catalog, err)
		catalogs = append(catalogs, result)
		if len(result.uids) > 0 {
			candidate := resolvePackageUIDsFromCatalog(result.source, result.uids, requested, match)
			resolvedRequested := len(requested) - len(candidate.UnresolvedPackages)
			if len(requested) == 0 {
				resolvedRequested = len(candidate.UIDs)
			}
			if best.Source == "" ||
				len(candidate.UnresolvedPackages) < bestUnresolved ||
				(len(candidate.UnresolvedPackages) == bestUnresolved && resolvedRequested > bestResolvedRequested) {
				best = candidate
				bestResolvedRequested = resolvedRequested
				bestUnresolved = len(candidate.UnresolvedPackages)
			}
			if !probeAll && (len(requested) == 0 || len(candidate.UnresolvedPackages) == 0) {
				break
			}
		}
	}

	if best.Source == "" {
		best.RequestedPackages = append([]string(nil), requested...)
		best.UnresolvedPackages = append([]string(nil), requested...)
	}
	best.Sources = sourceStatuses(catalogs)
	best.Errors = sourceErrors(catalogs)
	return best
}

func resolvePackageUIDsFromCatalogs(catalogs []packageUIDCatalogResult, requested []string, match func(string) bool) PackageUIDResolution {
	var best PackageUIDResolution
	bestResolvedRequested := -1
	bestUnresolved := len(requested) + 1
	statuses := sourceStatuses(catalogs)
	errors := sourceErrors(catalogs)

	for _, catalog := range catalogs {
		if len(catalog.uids) == 0 {
			continue
		}
		result := resolvePackageUIDsFromCatalog(catalog.source, catalog.uids, requested, match)
		resolvedRequested := len(requested) - len(result.UnresolvedPackages)
		if len(requested) == 0 {
			resolvedRequested = len(result.UIDs)
		}
		if best.Source == "" ||
			len(result.UnresolvedPackages) < bestUnresolved ||
			(len(result.UnresolvedPackages) == bestUnresolved && resolvedRequested > bestResolvedRequested) {
			best = result
			bestResolvedRequested = resolvedRequested
			bestUnresolved = len(result.UnresolvedPackages)
		}
		if len(requested) == 0 || len(result.UnresolvedPackages) == 0 {
			break
		}
	}

	if best.Source == "" {
		best.RequestedPackages = append([]string(nil), requested...)
		best.UnresolvedPackages = append([]string(nil), requested...)
	}
	best.Sources = statuses
	best.Errors = errors
	return best
}

func resolvePackageUIDsFromCatalog(source string, catalog map[string]int, requested []string, match func(string) bool) PackageUIDResolution {
	userIDs := discoverAndroidUserIDs()
	seenUIDs := map[int]bool{}
	resolvedPackages := map[string]bool{}
	uidInts := make([]int, 0)

	pkgNames := make([]string, 0, len(catalog))
	for pkgName := range catalog {
		pkgNames = append(pkgNames, pkgName)
	}
	sort.Strings(pkgNames)
	for _, pkgName := range pkgNames {
		if !match(pkgName) {
			continue
		}
		appID := catalog[pkgName] % 100000
		for _, userID := range userIDs {
			fullUID := userID*100000 + appID
			if !seenUIDs[fullUID] {
				seenUIDs[fullUID] = true
				uidInts = append(uidInts, fullUID)
			}
		}
		resolvedPackages[pkgName] = true
	}
	sort.Ints(uidInts)

	uids := make([]string, 0, len(uidInts))
	for _, uid := range uidInts {
		uids = append(uids, strconv.Itoa(uid))
	}

	unresolved := make([]string, 0)
	for _, pkgName := range requested {
		if !resolvedPackages[pkgName] {
			unresolved = append(unresolved, pkgName)
		}
	}
	return PackageUIDResolution{
		Source:             source,
		UIDs:               uids,
		UIDString:          strings.Join(uids, " "),
		RequestedPackages:  append([]string(nil), requested...),
		UnresolvedPackages: unresolved,
	}
}

func loadPackageUIDCatalogs(probeAll bool) []packageUIDCatalogResult {
	loaders := packageUIDCatalogLoaders()
	results := make([]packageUIDCatalogResult, 0, len(loaders))
	for _, loader := range loaders {
		catalog, err := loader.load()
		result := newPackageUIDCatalogResult(loader.source, catalog, err)
		results = append(results, result)
		if !probeAll && result.status.Available {
			break
		}
	}
	return results
}

func packageUIDCatalogLoaders() []struct {
	source string
	load   func() (map[string]int, error)
} {
	return []struct {
		source string
		load   func() (map[string]int, error)
	}{
		{"packages.list", loadPackagesListCatalog},
		{"cmd_package", loadCmdPackageCatalog},
		{"cmd_package_shell", loadCmdPackageShellCatalog},
	}
}

func newPackageUIDCatalogResult(source string, catalog map[string]int, err error) packageUIDCatalogResult {
	result := packageUIDCatalogResult{
		source: source,
		uids:   catalog,
		status: PackageUIDSourceStatus{Source: source, Entries: len(catalog)},
	}
	if err != nil {
		result.errText = err.Error()
		result.status.Error = err.Error()
	} else {
		result.status.Available = true
	}
	return result
}

func sourceStatuses(catalogs []packageUIDCatalogResult) []PackageUIDSourceStatus {
	statuses := make([]PackageUIDSourceStatus, 0, len(catalogs))
	for _, catalog := range catalogs {
		statuses = append(statuses, catalog.status)
	}
	return statuses
}

func sourceErrors(catalogs []packageUIDCatalogResult) []string {
	errors := make([]string, 0)
	for _, catalog := range catalogs {
		if catalog.errText != "" {
			errors = append(errors, catalog.source+": "+catalog.errText)
		}
	}
	return errors
}

func loadPackagesListCatalog() (map[string]int, error) {
	data, err := os.ReadFile(packageListPath)
	if err != nil {
		return nil, err
	}
	return parsePackagesListUIDs(string(data))
}

func loadCmdPackageCatalog() (map[string]int, error) {
	out, err := runPackageUIDCommand(false)
	if err != nil {
		return nil, err
	}
	return parseCmdPackageUIDs(out)
}

func loadCmdPackageShellCatalog() (map[string]int, error) {
	out, err := runPackageUIDCommand(true)
	if err != nil {
		return nil, err
	}
	return parseCmdPackageUIDs(out)
}

func defaultPackageUIDCommand(asShell bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), packageUIDCommandTimeout)
	defer cancel()
	name := "cmd"
	args := []string{"package", "list", "packages", "-U"}
	if asShell {
		name = "su"
		args = []string{"-lp", "2000", "-c", "cmd package list packages -U"}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return strings.TrimSpace(string(out)), ctx.Err()
	}
	return strings.TrimSpace(string(out)), err
}

func parsePackagesListUIDs(data string) (map[string]int, error) {
	uids := map[string]int{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
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
		uids[fields[0]] = uid
	}
	if len(uids) == 0 {
		return nil, fmt.Errorf("no package UID entries found")
	}
	return uids, nil
}

func parseCmdPackageUIDs(data string) (map[string]int, error) {
	uids := map[string]int{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		var pkgName string
		var uid int
		uidSet := false
		for _, field := range fields {
			key, value, ok := strings.Cut(field, ":")
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "package":
				pkgName = strings.TrimSpace(value)
			case "uid", "userid":
				parsed, err := strconv.Atoi(strings.TrimSpace(value))
				if err == nil {
					uid = parsed
					uidSet = true
				}
			}
		}
		if pkgName != "" && uidSet {
			uids[pkgName] = uid
		}
	}
	if len(uids) == 0 {
		return nil, fmt.Errorf("no package UID entries found")
	}
	return uids, nil
}

func discoverAndroidUserIDs() []int {
	userIDs := []int{0}
	entries, err := os.ReadDir(dataUserPath)
	if err != nil {
		return userIDs
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if uid, parseErr := strconv.Atoi(e.Name()); parseErr == nil && uid > 0 {
			userIDs = append(userIDs, uid)
		}
	}
	sort.Ints(userIDs)
	return userIDs
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
