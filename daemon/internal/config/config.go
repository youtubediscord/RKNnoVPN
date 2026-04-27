package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const CurrentSchemaVersion = 5

// Config is the canonical daemon configuration.
type Config struct {
	SchemaVersion int                     `json:"schema_version"`
	Proxy         ProxyConfig             `json:"proxy"`
	Transport     TransportConfig         `json:"transport"`
	Node          NodeConfig              `json:"node"`
	Profile       ProfileProjectionConfig `json:"-"`
	RuntimeV2     RuntimeV2Config         `json:"runtime_v2,omitempty"`
	Routing       RoutingConfig           `json:"routing"`
	Apps          AppsConfig              `json:"apps"`
	DNS           DNSConfig               `json:"dns"`
	IPv6          IPv6Config              `json:"ipv6"`
	Sharing       SharingConfig           `json:"sharing,omitempty"`
	Health        HealthConfig            `json:"health"`
	Rescue        RescueConfig            `json:"rescue"`
	Autostart     bool                    `json:"autostart"`
}

// ProxyConfig controls the sing-box proxy listener ports.
type ProxyConfig struct {
	Mode       string `json:"mode"` // "tproxy" (matches config.json proxy.mode)
	TProxyPort int    `json:"tproxy_port"`
	DNSPort    int    `json:"dns_port"`
	GID        int    `json:"gid"`      // core process GID (matches config.json proxy.gid)
	Mark       int    `json:"mark"`     // fwmark for policy routing (matches config.json proxy.mark)
	APIPort    int    `json:"api_port"` // 0 disables sing-box Clash REST API
}

// TransportConfig controls the outbound protocol transport layer.
type TransportConfig struct {
	Protocol    string            `json:"protocol"`    // "reality", "ws", "grpc", "tcp", "h2"
	TLSServer   string            `json:"tls_server"`  // SNI for TLS
	Fingerprint string            `json:"fingerprint"` // uTLS fingerprint
	Extra       map[string]string `json:"extra"`       // protocol-specific fields
}

// NodeConfig describes the remote proxy server.
type NodeConfig struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"` // "vless", "trojan", "vmess", "shadowsocks", "socks", "hysteria2", "tuic", "wireguard"
	UUID     string `json:"uuid"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Flow     string `json:"flow"` // e.g. "xtls-rprx-vision"

	// Shadowsocks-specific fields.
	SSMethod     string `json:"ss_method,omitempty"`
	SSPlugin     string `json:"ss_plugin,omitempty"`
	SSPluginOpts string `json:"ss_plugin_opts,omitempty"`

	// SOCKS-specific fields.
	SocksVersion string `json:"socks_version,omitempty"`
	Network      string `json:"network,omitempty"`

	// Hysteria2-specific fields.
	ServerPorts  []string `json:"server_ports,omitempty"`
	ObfsType     string   `json:"obfs_type,omitempty"`
	ObfsPassword string   `json:"obfs_password,omitempty"`

	// VMess-specific fields.
	AlterID  int    `json:"alter_id,omitempty"`
	Security string `json:"security,omitempty"` // vmess encryption

	// REALITY-specific fields.
	RealityPublicKey string `json:"reality_public_key,omitempty"`
	RealityShortID   string `json:"reality_short_id,omitempty"`

	// WireGuard outbound fields. These are rendered as a sing-box outbound,
	// never as a kernel/Android WireGuard interface.
	WGPrivateKey    string   `json:"wg_private_key,omitempty"`
	WGPeerPublicKey string   `json:"wg_peer_public_key,omitempty"`
	WGPresharedKey  string   `json:"wg_preshared_key,omitempty"`
	WGLocalAddress  []string `json:"wg_local_address,omitempty"`
	WGAllowedIPs    string   `json:"wg_allowed_ips,omitempty"`
	WGMTU           int      `json:"wg_mtu,omitempty"`
	WGReserved      []int    `json:"wg_reserved,omitempty"`
}

// ProfileProjectionConfig stores profile projection data that the daemon itself does not need
// to understand in depth, but must persist without loss.
type ProfileProjectionConfig struct {
	ID            string            `json:"id,omitempty"`
	Name          string            `json:"name,omitempty"`
	ActiveNodeID  string            `json:"active_node_id,omitempty"`
	Nodes         []json.RawMessage `json:"nodes,omitempty"`
	Subscriptions []json.RawMessage `json:"subscriptions,omitempty"`
	Tun           json.RawMessage   `json:"tun,omitempty"`
	Inbounds      json.RawMessage   `json:"inbounds,omitempty"`
	Extra         json.RawMessage   `json:"extra,omitempty"`
}

// RuntimeV2Config stores reliability-first backend selection state for the
// side-by-side v2 runtime path.
type RuntimeV2Config struct {
	BackendKind    string `json:"backend_kind,omitempty"`
	FallbackPolicy string `json:"fallback_policy,omitempty"`
}

// ProfileInboundsConfig stores daemon profile local inbound settings that the daemon
// may use for localhost-only helper ports.
type ProfileInboundsConfig struct {
	SocksPort int  `json:"socksPort"`
	HTTPPort  int  `json:"httpPort"`
	AllowLAN  bool `json:"allowLan"`
}

type ProfileNodeSourceConfig struct {
	Type        string `json:"type,omitempty"`
	URL         string `json:"url,omitempty"`
	ProviderKey string `json:"providerKey,omitempty"`
	LastSeenAt  int64  `json:"lastSeenAt,omitempty"`
}

type ProfileSubscriptionConfig struct {
	ProviderKey       string `json:"providerKey,omitempty"`
	URL               string `json:"url,omitempty"`
	Name              string `json:"name,omitempty"`
	LastFetchedAt     int64  `json:"lastFetchedAt,omitempty"`
	LastSeenNodeCount int    `json:"lastSeenNodeCount,omitempty"`
	StaleNodeCount    int    `json:"staleNodeCount,omitempty"`
	UploadBytes       int64  `json:"uploadBytes,omitempty"`
	DownloadBytes     int64  `json:"downloadBytes,omitempty"`
	TotalBytes        int64  `json:"totalBytes,omitempty"`
	ExpireTimestamp   int64  `json:"expireTimestamp,omitempty"`
	ParseFailures     int    `json:"parseFailures,omitempty"`
}

type profileNodeValidationConfig struct {
	ID       string                   `json:"id,omitempty"`
	Protocol string                   `json:"protocol,omitempty"`
	Server   string                   `json:"server,omitempty"`
	Port     int                      `json:"port,omitempty"`
	Stale    bool                     `json:"stale,omitempty"`
	Source   *ProfileNodeSourceConfig `json:"source,omitempty"`
}

// RoutingConfig controls traffic routing rules.
type RoutingConfig struct {
	Mode             string   `json:"mode"` // "all", "whitelist", "blacklist", "rules", "direct"
	BypassLAN        bool     `json:"bypass_lan"`
	BypassChina      bool     `json:"bypass_china"` // matches config.json routing.bypass_china
	BypassRussia     bool     `json:"bypass_russia"`
	BlockAds         bool     `json:"block_ads"`
	CustomDirect     []string `json:"custom_direct"` // domains/IPs to route directly
	CustomProxy      []string `json:"custom_proxy"`  // domains/IPs to force through proxy
	CustomBlock      []string `json:"custom_block"`  // domains/IPs to block
	AlwaysDirectApps []string `json:"always_direct_apps,omitempty"`
	GeoIPPath        string   `json:"geoip_path"`
	GeoSitePath      string   `json:"geosite_path"`
}

// AppsConfig controls per-app routing (Android split tunnel).
type AppsConfig struct {
	Mode      string            `json:"mode"`       // "all", "whitelist", "blacklist", "off"
	Packages  []string          `json:"list"`       // package names for whitelist/blacklist (matches config.json apps.list)
	AppGroups map[string]string `json:"app_groups"` // package name -> profile node group outbound
}

// DNSConfig controls DNS resolution.
type DNSConfig struct {
	HijackPerUID bool   `json:"hijack_per_uid"` // per-UID DNS hijack (matches config.json dns.hijack_per_uid)
	ProxyDNS     string `json:"proxy_dns"`      // DoH URL routed via proxy (matches config.json dns.proxy_dns)
	DirectDNS    string `json:"direct_dns"`     // DoH URL for direct domains (matches config.json dns.direct_dns)
	BootstrapIP  string `json:"bootstrap_ip"`   // IP-literal for bootstrapping DoH
	BlockQUICDNS bool   `json:"block_quic_dns"` // block QUIC DNS (matches config.json dns.block_quic_dns)
	FakeIP       bool   `json:"fake_ip"`        // use fake-ip strategy
}

// IPv6Config controls IPv6 behavior.
type IPv6Config struct {
	Mode string `json:"mode"` // "mirror", "disable", etc. (matches config.json ipv6.mode)
}

// SharingConfig controls forwarded hotspot/tethering client traffic.
// It is explicit because forwarding other devices is a different privacy
// surface from per-app local TPROXY.
type SharingConfig struct {
	Enabled    bool     `json:"enabled"`
	Interfaces []string `json:"interfaces,omitempty"`
}

// HealthConfig controls automatic health checking.
type HealthConfig struct {
	Enabled            bool     `json:"enabled"` // matches config.json health.enabled
	IntervalSec        int      `json:"interval_sec"`
	Threshold          int      `json:"threshold"` // failure threshold (matches config.json health.threshold)
	URL                string   `json:"check_url"` // URL to probe (matches config.json health.check_url)
	TimeoutSec         int      `json:"timeout_sec"`
	DNSProbeDomains    []string `json:"dns_probe_domains,omitempty"`
	EgressURLs         []string `json:"egress_urls,omitempty"`
	DNSIsHardReadiness bool     `json:"dns_is_hard_readiness"`
}

// RescueConfig controls automatic fallback on persistent failures.
type RescueConfig struct {
	Enabled     bool `json:"enabled"`      // matches config.json rescue.enabled
	MaxAttempts int  `json:"max_attempts"` // consecutive failures before rescue (matches config.json rescue.max_attempts)
	CooldownSec int  `json:"cooldown_sec"` // wait time before retrying after rescue
}

// NodeProfile is a resolved node profile ready for sing-box config rendering.
// It merges NodeConfig + TransportConfig into a flat structure.
type NodeProfile struct {
	ID              string            `json:"id,omitempty"`
	Name            string            `json:"name,omitempty"`
	Group           string            `json:"group,omitempty"`
	Tag             string            `json:"tag,omitempty"`
	Protocol        string            `json:"protocol"`
	Address         string            `json:"address"`
	Port            int               `json:"port"`
	UUID            string            `json:"uuid"`
	Username        string            `json:"username,omitempty"`
	Password        string            `json:"password,omitempty"`
	Flow            string            `json:"flow,omitempty"`
	Transport       string            `json:"transport"`
	TLSServer       string            `json:"tls_server"`
	Fingerprint     string            `json:"fingerprint"`
	SSMethod        string            `json:"ss_method,omitempty"`
	SSPlugin        string            `json:"ss_plugin,omitempty"`
	SSPluginOpts    string            `json:"ss_plugin_opts,omitempty"`
	SocksVersion    string            `json:"socks_version,omitempty"`
	Network         string            `json:"network,omitempty"`
	ServerPorts     []string          `json:"server_ports,omitempty"`
	ObfsType        string            `json:"obfs_type,omitempty"`
	ObfsPassword    string            `json:"obfs_password,omitempty"`
	AlterID         int               `json:"alter_id,omitempty"`
	Security        string            `json:"security,omitempty"`
	RealityPubKey   string            `json:"reality_public_key,omitempty"`
	RealityShortID  string            `json:"reality_short_id,omitempty"`
	WGPrivateKey    string            `json:"wg_private_key,omitempty"`
	WGPeerPublicKey string            `json:"wg_peer_public_key,omitempty"`
	WGPresharedKey  string            `json:"wg_preshared_key,omitempty"`
	WGLocalAddress  []string          `json:"wg_local_address,omitempty"`
	WGAllowedIPs    string            `json:"wg_allowed_ips,omitempty"`
	WGMTU           int               `json:"wg_mtu,omitempty"`
	WGReserved      []int             `json:"wg_reserved,omitempty"`
	Extra           map[string]string `json:"extra,omitempty"`
	Stale           bool              `json:"stale,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		SchemaVersion: CurrentSchemaVersion,
		Proxy: ProxyConfig{
			Mode:       "tproxy",
			TProxyPort: 10853,
			DNSPort:    10856,
			GID:        23333,
			Mark:       8227,
			APIPort:    0,
		},
		Transport: TransportConfig{
			Protocol:    "reality",
			Fingerprint: "chrome",
		},
		Node: NodeConfig{
			Protocol: "vless",
			Port:     443,
		},
		Profile: ProfileProjectionConfig{
			ID:   "default",
			Name: "Default",
		},
		RuntimeV2: RuntimeV2Config{
			BackendKind:    "ROOT_TPROXY",
			FallbackPolicy: "OFFER_RESET",
		},
		Routing: RoutingConfig{
			Mode:        "whitelist",
			BypassLAN:   true,
			GeoIPPath:   "/data/adb/rknnovpn/data/geoip.db",
			GeoSitePath: "/data/adb/rknnovpn/data/geosite.db",
		},
		Apps: AppsConfig{
			Mode: "whitelist",
		},
		DNS: DNSConfig{
			HijackPerUID: true,
			ProxyDNS:     "https://1.1.1.1/dns-query",
			DirectDNS:    "https://dns.google/dns-query",
			BootstrapIP:  "1.1.1.1",
			BlockQUICDNS: true,
			FakeIP:       false,
		},
		IPv6: IPv6Config{
			Mode: "mirror",
		},
		Sharing: SharingConfig{
			Enabled: false,
		},
		Health: HealthConfig{
			Enabled:         true,
			IntervalSec:     30,
			Threshold:       3,
			URL:             "https://www.gstatic.com/generate_204",
			TimeoutSec:      5,
			DNSProbeDomains: []string{"connectivitycheck.gstatic.com", "cloudflare.com", "example.com"},
			EgressURLs: []string{
				"https://www.gstatic.com/generate_204",
				"https://cp.cloudflare.com/generate_204",
			},
			DNSIsHardReadiness: false,
		},
		Rescue: RescueConfig{
			Enabled:     true,
			MaxAttempts: 3,
			CooldownSec: 60,
		},
		Autostart: false,
	}
}

func (c *Config) SharingModeEnv() string {
	if c != nil && c.Sharing.Enabled {
		return "hotspot"
	}
	return "off"
}

func (c *Config) SharingInterfacesEnv() string {
	if c == nil || len(c.Sharing.Interfaces) == 0 {
		return ""
	}
	values := make([]string, 0, len(c.Sharing.Interfaces))
	for _, iface := range c.Sharing.Interfaces {
		iface = strings.TrimSpace(iface)
		if iface != "" {
			values = append(values, iface)
		}
	}
	return strings.Join(values, " ")
}

// defaultProfileProjectionConfig returns empty profile projection defaults.
func defaultProfileProjectionConfig() ProfileProjectionConfig {
	return ProfileProjectionConfig{
		ID:   "default",
		Name: "Default",
	}
}

// ResolveProfileInbounds returns the effective profile inbound settings with
// defaults applied even when profile inbounds are absent.
func (c *Config) ResolveProfileInbounds() ProfileInboundsConfig {
	result := ProfileInboundsConfig{
		SocksPort: 0,
		HTTPPort:  0,
		AllowLAN:  false,
	}
	if len(c.Profile.Inbounds) == 0 {
		return result
	}
	var decoded ProfileInboundsConfig
	if err := json.Unmarshal(c.Profile.Inbounds, &decoded); err != nil {
		return result
	}
	if decoded.SocksPort > 0 {
		result.SocksPort = decoded.SocksPort
	}
	if decoded.HTTPPort > 0 {
		result.HTTPPort = decoded.HTTPPort
	}
	// Helper inbounds are diagnostics/local-control surfaces and are always
	// localhost-only in the root runtime.
	result.AllowLAN = false
	return result
}

// Load reads a Config from the given JSON file path.
// If the file does not exist, it returns DefaultConfig.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	cfg := DefaultConfig()

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: validate: %w", err)
	}

	return cfg, nil
}

// Save writes the Config as formatted JSON to the given file path,
// creating parent directories as needed.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}

	if c.SchemaVersion == 0 {
		c.SchemaVersion = CurrentSchemaVersion
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	data = append(data, '\n')

	if err := writeFileAtomic(path, data, 0600, "config"); err != nil {
		return err
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode, label string) error {
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("%s: open %s: %w", label, tmpPath, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s: write %s: %w", label, tmpPath, err)
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s: chmod %s: %w", label, tmpPath, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s: sync %s: %w", label, tmpPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s: close %s: %w", label, tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s: rename %s: %w", label, path, err)
	}
	syncDirBestEffort(filepath.Dir(path))
	return nil
}

func validateProfileProjectionConfig(profile ProfileProjectionConfig) error {
	if len(profile.Inbounds) > 0 {
		var inbounds ProfileInboundsConfig
		if err := json.Unmarshal(profile.Inbounds, &inbounds); err != nil {
			return fmt.Errorf("profile.inbounds invalid: %w", err)
		}
		if inbounds.SocksPort < 0 || inbounds.SocksPort > 65535 {
			return fmt.Errorf("profile.inbounds.socksPort must be 0-65535, got %d", inbounds.SocksPort)
		}
		if inbounds.HTTPPort < 0 || inbounds.HTTPPort > 65535 {
			return fmt.Errorf("profile.inbounds.httpPort must be 0-65535, got %d", inbounds.HTTPPort)
		}
		if inbounds.AllowLAN {
			return fmt.Errorf("profile.inbounds.allowLan is not supported by root helper inbounds")
		}
	}
	subscriptionProviderKeys := make(map[string]bool, len(profile.Subscriptions))
	for index, raw := range profile.Subscriptions {
		if len(bytes.TrimSpace(raw)) == 0 {
			return fmt.Errorf("profile.subscriptions[%d] is empty", index)
		}
		var subscription ProfileSubscriptionConfig
		if err := json.Unmarshal(raw, &subscription); err != nil {
			return fmt.Errorf("profile.subscriptions[%d] invalid: %w", index, err)
		}
		key := strings.TrimSpace(subscription.ProviderKey)
		if key != "" {
			subscriptionProviderKeys[key] = true
		}
	}
	for index, raw := range profile.Nodes {
		if len(bytes.TrimSpace(raw)) == 0 {
			return fmt.Errorf("profile.nodes[%d] is empty", index)
		}
		var node profileNodeValidationConfig
		if err := json.Unmarshal(raw, &node); err != nil {
			return fmt.Errorf("profile.nodes[%d] invalid: %w", index, err)
		}
		if node.Port < 0 || node.Port > 65535 {
			return fmt.Errorf("profile.nodes[%d].port must be 0-65535, got %d", index, node.Port)
		}
		if node.Source == nil {
			return fmt.Errorf("profile.nodes[%d].source is required", index)
		}
		source := normalizedProfileNodeSource(node.Stale, node.Source)
		switch source.Type {
		case "MANUAL":
			if node.Stale {
				return fmt.Errorf("profile.nodes[%d].stale requires subscription source", index)
			}
		case "SUBSCRIPTION":
			if strings.TrimSpace(source.ProviderKey) == "" {
				return fmt.Errorf("profile.nodes[%d].source.providerKey is required for subscription nodes", index)
			}
			if !subscriptionProviderKeys[source.ProviderKey] {
				return fmt.Errorf("profile.nodes[%d].source.providerKey %q is missing from profile.subscriptions", index, source.ProviderKey)
			}
			if source.LastSeenAt < 0 {
				return fmt.Errorf("profile.nodes[%d].source.lastSeenAt must be >= 0", index)
			}
		default:
			return fmt.Errorf("profile.nodes[%d].source.type must be MANUAL or SUBSCRIPTION, got %q", index, source.Type)
		}
	}
	for index, raw := range profile.Subscriptions {
		var subscription ProfileSubscriptionConfig
		_ = json.Unmarshal(raw, &subscription)
		if strings.TrimSpace(subscription.ProviderKey) == "" {
			return fmt.Errorf("profile.subscriptions[%d].providerKey is required", index)
		}
		if strings.TrimSpace(subscription.URL) == "" {
			return fmt.Errorf("profile.subscriptions[%d].url is required", index)
		}
		if subscription.LastFetchedAt < 0 {
			return fmt.Errorf("profile.subscriptions[%d].lastFetchedAt must be >= 0", index)
		}
		if subscription.LastSeenNodeCount < 0 {
			return fmt.Errorf("profile.subscriptions[%d].lastSeenNodeCount must be >= 0", index)
		}
		if subscription.StaleNodeCount < 0 {
			return fmt.Errorf("profile.subscriptions[%d].staleNodeCount must be >= 0", index)
		}
		if subscription.UploadBytes < 0 || subscription.DownloadBytes < 0 || subscription.TotalBytes < 0 {
			return fmt.Errorf("profile.subscriptions[%d] traffic counters must be >= 0", index)
		}
		if subscription.ExpireTimestamp < 0 {
			return fmt.Errorf("profile.subscriptions[%d].expireTimestamp must be >= 0", index)
		}
		if subscription.ParseFailures < 0 {
			return fmt.Errorf("profile.subscriptions[%d].parseFailures must be >= 0", index)
		}
	}
	return nil
}

func syncDirBestEffort(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	_ = f.Sync()
}

// Validate checks the Config for obvious misconfigurations.
func (c *Config) Validate() error {
	if c.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("schema_version must be %d, got %d", CurrentSchemaVersion, c.SchemaVersion)
	}
	if err := validateProfileProjectionConfig(c.Profile); err != nil {
		return err
	}
	if c.Proxy.TProxyPort < 1 || c.Proxy.TProxyPort > 65535 {
		return fmt.Errorf("proxy.tproxy_port must be 1-65535, got %d", c.Proxy.TProxyPort)
	}
	if c.Proxy.DNSPort < 1 || c.Proxy.DNSPort > 65535 {
		return fmt.Errorf("proxy.dns_port must be 1-65535, got %d", c.Proxy.DNSPort)
	}
	if c.Proxy.APIPort < 0 || c.Proxy.APIPort > 65535 {
		return fmt.Errorf("proxy.api_port must be 0-65535, got %d", c.Proxy.APIPort)
	}

	validProto := map[string]bool{
		"vless": true, "trojan": true, "vmess": true, "shadowsocks": true, "socks": true,
		"hysteria2": true, "tuic": true, "wireguard": true,
	}
	if c.Node.Protocol != "" && !validProto[c.Node.Protocol] {
		return fmt.Errorf("node.protocol must be one of vless/trojan/vmess/shadowsocks/socks/hysteria2/tuic/wireguard, got %q", c.Node.Protocol)
	}
	if c.Node.Address != "" && c.Node.Protocol == "" {
		return fmt.Errorf("node.protocol is required when node.address is set")
	}
	switch c.Node.Protocol {
	case "vless", "vmess":
		if c.Node.Address != "" && c.Node.UUID == "" {
			return fmt.Errorf("node.uuid is required for %s", c.Node.Protocol)
		}
	case "trojan", "shadowsocks":
		if c.Node.Address != "" && c.Node.UUID == "" && c.Node.Password == "" {
			return fmt.Errorf("node password is required for %s", c.Node.Protocol)
		}
	case "hysteria2":
		if c.Node.Address != "" && c.Node.Password == "" && c.Node.UUID == "" {
			return fmt.Errorf("node.password is required for hysteria2")
		}
	case "tuic":
		if c.Node.Address != "" && (c.Node.UUID == "" || c.Node.Password == "") {
			return fmt.Errorf("node.uuid and node.password are required for tuic")
		}
	case "wireguard":
		if c.Node.Address != "" && (c.Node.WGPrivateKey == "" || c.Node.WGPeerPublicKey == "" || len(c.Node.WGLocalAddress) == 0) {
			return fmt.Errorf("node.wireguard keys and local address are required for wireguard")
		}
	}
	if c.Transport.Protocol != "" {
		validTransport := map[string]bool{
			"tcp": true, "reality": true, "ws": true, "grpc": true,
			"http": true, "h2": true, "quic": true, "httpupgrade": true,
		}
		if !validTransport[c.Transport.Protocol] {
			return fmt.Errorf("transport.protocol %q is not supported by sing-box V2Ray transport", c.Transport.Protocol)
		}
	}

	validRoutingMode := map[string]bool{
		"all": true, "whitelist": true, "blacklist": true, "rules": true, "direct": true,
	}
	if !validRoutingMode[c.Routing.Mode] {
		return fmt.Errorf("routing.mode must be all/whitelist/blacklist/rules/direct, got %q", c.Routing.Mode)
	}

	validAppMode := map[string]bool{
		"all": true, "whitelist": true, "blacklist": true, "off": true,
	}
	if !validAppMode[c.Apps.Mode] {
		return fmt.Errorf("apps.mode must be all/whitelist/blacklist/off, got %q", c.Apps.Mode)
	}

	if c.RuntimeV2.BackendKind != "" {
		validBackend := map[string]bool{
			"ROOT_TPROXY": true,
		}
		if !validBackend[c.RuntimeV2.BackendKind] {
			return fmt.Errorf("runtime_v2.backend_kind must be ROOT_TPROXY, got %q", c.RuntimeV2.BackendKind)
		}
	}

	if c.RuntimeV2.FallbackPolicy != "" {
		validFallback := map[string]bool{
			"OFFER_RESET":       true,
			"STAY_ON_SELECTED":  true,
			"AUTO_RESET_ROOTED": true,
		}
		if !validFallback[c.RuntimeV2.FallbackPolicy] {
			return fmt.Errorf("runtime_v2.fallback_policy must be OFFER_RESET/STAY_ON_SELECTED/AUTO_RESET_ROOTED, got %q", c.RuntimeV2.FallbackPolicy)
		}
	}

	if c.Health.IntervalSec < 0 {
		return fmt.Errorf("health.interval_sec must be >= 0, got %d", c.Health.IntervalSec)
	}
	if c.Health.TimeoutSec < 1 {
		return fmt.Errorf("health.timeout_sec must be >= 1, got %d", c.Health.TimeoutSec)
	}
	if c.Rescue.MaxAttempts < 1 {
		return fmt.Errorf("rescue.max_attempts must be >= 1, got %d", c.Rescue.MaxAttempts)
	}
	if (c.Routing.BypassChina || c.Routing.BypassRussia) && (c.Routing.GeoIPPath == "" || c.Routing.GeoSitePath == "") {
		return fmt.Errorf("routing geo bypass requires both geoip_path and geosite_path to be set")
	}
	if c.Routing.BlockAds && c.Routing.GeoSitePath == "" {
		return fmt.Errorf("routing block_ads requires geosite_path to be set")
	}

	return nil
}

// ResolveProfile merges Node + Transport into a flat NodeProfile
// suitable for rendering a sing-box outbound.
func (c *Config) ResolveProfile() *NodeProfile {
	return &NodeProfile{
		Protocol:        c.Node.Protocol,
		Address:         c.Node.Address,
		Port:            c.Node.Port,
		UUID:            c.Node.UUID,
		Username:        c.Node.Username,
		Password:        c.Node.Password,
		Flow:            c.Node.Flow,
		Transport:       c.Transport.Protocol,
		TLSServer:       c.Transport.TLSServer,
		Fingerprint:     c.Transport.Fingerprint,
		SSMethod:        c.Node.SSMethod,
		SSPlugin:        c.Node.SSPlugin,
		SSPluginOpts:    c.Node.SSPluginOpts,
		SocksVersion:    c.Node.SocksVersion,
		Network:         c.Node.Network,
		ServerPorts:     c.Node.ServerPorts,
		ObfsType:        c.Node.ObfsType,
		ObfsPassword:    c.Node.ObfsPassword,
		AlterID:         c.Node.AlterID,
		Security:        c.Node.Security,
		RealityPubKey:   c.Node.RealityPublicKey,
		RealityShortID:  c.Node.RealityShortID,
		WGPrivateKey:    c.Node.WGPrivateKey,
		WGPeerPublicKey: c.Node.WGPeerPublicKey,
		WGPresharedKey:  c.Node.WGPresharedKey,
		WGLocalAddress:  append([]string(nil), c.Node.WGLocalAddress...),
		WGAllowedIPs:    c.Node.WGAllowedIPs,
		WGMTU:           c.Node.WGMTU,
		WGReserved:      append([]int(nil), c.Node.WGReserved...),
		Extra:           c.Transport.Extra,
	}
}

func normalizeProfileProjectionConfig(profile ProfileProjectionConfig) ProfileProjectionConfig {
	if profile.ID == "" {
		profile.ID = defaultProfileProjectionConfig().ID
	}
	if profile.Name == "" {
		profile.Name = defaultProfileProjectionConfig().Name
	}
	if profile.Nodes == nil {
		profile.Nodes = []json.RawMessage{}
	}
	profile.Nodes = normalizeProfileNodes(profile.Nodes)
	if profile.Subscriptions == nil {
		profile.Subscriptions = []json.RawMessage{}
	}
	profile.Subscriptions = normalizeProfileSubscriptions(profile.Subscriptions)
	return profile
}

func normalizeProfileSubscriptions(subscriptions []json.RawMessage) []json.RawMessage {
	normalized := make([]json.RawMessage, 0, len(subscriptions))
	for _, raw := range subscriptions {
		var subscription ProfileSubscriptionConfig
		if err := json.Unmarshal(raw, &subscription); err != nil {
			normalized = append(normalized, raw)
			continue
		}
		subscription.ProviderKey = strings.TrimSpace(subscription.ProviderKey)
		subscription.URL = strings.TrimSpace(subscription.URL)
		subscription.Name = strings.TrimSpace(subscription.Name)
		normalizedRaw, err := json.Marshal(subscription)
		if err != nil {
			normalized = append(normalized, raw)
			continue
		}
		normalized = append(normalized, normalizedRaw)
	}
	return normalized
}

func normalizeProfileNodes(nodes []json.RawMessage) []json.RawMessage {
	normalized := make([]json.RawMessage, 0, len(nodes))
	for _, raw := range nodes {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(raw, &node); err != nil {
			normalized = append(normalized, raw)
			continue
		}
		stale := false
		if value, ok := node["stale"]; ok {
			_ = json.Unmarshal(value, &stale)
		}
		var source *ProfileNodeSourceConfig
		if value, ok := node["source"]; ok {
			var decoded ProfileNodeSourceConfig
			if err := json.Unmarshal(value, &decoded); err == nil {
				source = &decoded
			}
		}
		if source == nil {
			normalized = append(normalized, raw)
			continue
		}
		normalizedSource := normalizedProfileNodeSource(stale, source)
		sourceRaw, err := json.Marshal(normalizedSource)
		if err != nil {
			normalized = append(normalized, raw)
			continue
		}
		node["source"] = sourceRaw
		normalizedRaw, err := json.Marshal(node)
		if err != nil {
			normalized = append(normalized, raw)
			continue
		}
		normalized = append(normalized, normalizedRaw)
	}
	return normalized
}

func normalizedProfileNodeSource(stale bool, source *ProfileNodeSourceConfig) ProfileNodeSourceConfig {
	if source == nil {
		return ProfileNodeSourceConfig{}
	}
	normalized := *source
	normalized.Type = strings.ToUpper(strings.TrimSpace(normalized.Type))
	normalized.ProviderKey = strings.TrimSpace(normalized.ProviderKey)
	normalized.URL = strings.TrimSpace(normalized.URL)
	return normalized
}

func (c *Config) SyncFromProfileProjection(authoritative bool) {
	if profile := ResolveActiveProfile(c); profile != nil {
		c.Node.Address = profile.Address
		c.Node.Port = profile.Port
		c.Node.Protocol = profile.Protocol
		c.Node.UUID = profile.UUID
		c.Node.Username = profile.Username
		c.Node.Password = profile.Password
		c.Node.Flow = profile.Flow
		c.Node.SSMethod = profile.SSMethod
		c.Node.SSPlugin = profile.SSPlugin
		c.Node.SSPluginOpts = profile.SSPluginOpts
		c.Node.SocksVersion = profile.SocksVersion
		c.Node.Network = profile.Network
		c.Node.ServerPorts = append([]string(nil), profile.ServerPorts...)
		c.Node.ObfsType = profile.ObfsType
		c.Node.ObfsPassword = profile.ObfsPassword
		c.Node.AlterID = profile.AlterID
		c.Node.Security = profile.Security
		c.Node.RealityPublicKey = profile.RealityPubKey
		c.Node.RealityShortID = profile.RealityShortID
		c.Node.WGPrivateKey = profile.WGPrivateKey
		c.Node.WGPeerPublicKey = profile.WGPeerPublicKey
		c.Node.WGPresharedKey = profile.WGPresharedKey
		c.Node.WGLocalAddress = append([]string(nil), profile.WGLocalAddress...)
		c.Node.WGAllowedIPs = profile.WGAllowedIPs
		c.Node.WGMTU = profile.WGMTU
		c.Node.WGReserved = append([]int(nil), profile.WGReserved...)
		c.Transport.Protocol = profile.Transport
		c.Transport.TLSServer = profile.TLSServer
		c.Transport.Fingerprint = profile.Fingerprint
		if profile.Extra == nil {
			c.Transport.Extra = map[string]string{}
		} else {
			c.Transport.Extra = profile.Extra
		}
		return
	}

	if authoritative {
		defaults := DefaultConfig()
		c.Node = defaults.Node
		c.Transport = defaults.Transport
	}
}
