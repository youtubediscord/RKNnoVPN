package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the canonical daemon configuration.
type Config struct {
	Proxy     ProxyConfig     `json:"proxy"`
	Transport TransportConfig `json:"transport"`
	Node      NodeConfig      `json:"node"`
	Panel     PanelConfig     `json:"-"`
	RuntimeV2 RuntimeV2Config `json:"runtime_v2,omitempty"`
	Routing   RoutingConfig   `json:"routing"`
	Apps      AppsConfig      `json:"apps"`
	DNS       DNSConfig       `json:"dns"`
	IPv6      IPv6Config      `json:"ipv6"`
	Health    HealthConfig    `json:"health"`
	Rescue    RescueConfig    `json:"rescue"`
	Autostart bool            `json:"autostart"`
}

// ProxyConfig controls the sing-box proxy listener ports.
type ProxyConfig struct {
	Mode       string `json:"mode"` // "tproxy" (matches config.json proxy.mode)
	TProxyPort int    `json:"tproxy_port"`
	DNSPort    int    `json:"dns_port"`
	GID        int    `json:"gid"`  // core process GID (matches config.json proxy.gid)
	Mark       int    `json:"mark"` // fwmark for policy routing (matches config.json proxy.mark)
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
	Protocol string `json:"protocol"` // "vless", "trojan", "vmess", "shadowsocks", "socks", "hysteria2", "tuic"
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
}

// PanelConfig stores APK-facing metadata that the daemon itself does not need
// to understand in depth, but must persist without loss.
type PanelConfig struct {
	ID           string            `json:"id,omitempty"`
	Name         string            `json:"name,omitempty"`
	ActiveNodeID string            `json:"active_node_id,omitempty"`
	Nodes        []json.RawMessage `json:"nodes,omitempty"`
	Tun          json.RawMessage   `json:"tun,omitempty"`
	Inbounds     json.RawMessage   `json:"inbounds,omitempty"`
	Extra        json.RawMessage   `json:"extra,omitempty"`
}

const panelFileName = "panel.json"

// RuntimeV2Config stores reliability-first backend selection state for the
// side-by-side v2 runtime path.
type RuntimeV2Config struct {
	BackendKind    string `json:"backend_kind,omitempty"`
	FallbackPolicy string `json:"fallback_policy,omitempty"`
}

// PanelInboundsConfig stores APK-owned local inbound settings that the daemon
// may use for localhost-only helper ports.
type PanelInboundsConfig struct {
	SocksPort int  `json:"socksPort"`
	HTTPPort  int  `json:"httpPort"`
	AllowLAN  bool `json:"allowLan"`
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
	Mode     string   `json:"mode"` // "all", "whitelist", "blacklist", "off"
	Packages []string `json:"list"` // package names for whitelist/blacklist (matches config.json apps.list)
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

// HealthConfig controls automatic health checking.
type HealthConfig struct {
	Enabled     bool   `json:"enabled"` // matches config.json health.enabled
	IntervalSec int    `json:"interval_sec"`
	Threshold   int    `json:"threshold"` // failure threshold (matches config.json health.threshold)
	URL         string `json:"check_url"` // URL to probe (matches config.json health.check_url)
	TimeoutSec  int    `json:"timeout_sec"`
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
	ID             string            `json:"id,omitempty"`
	Name           string            `json:"name,omitempty"`
	Tag            string            `json:"tag,omitempty"`
	Protocol       string            `json:"protocol"`
	Address        string            `json:"address"`
	Port           int               `json:"port"`
	UUID           string            `json:"uuid"`
	Username       string            `json:"username,omitempty"`
	Password       string            `json:"password,omitempty"`
	Flow           string            `json:"flow,omitempty"`
	Transport      string            `json:"transport"`
	TLSServer      string            `json:"tls_server"`
	Fingerprint    string            `json:"fingerprint"`
	SSMethod       string            `json:"ss_method,omitempty"`
	SSPlugin       string            `json:"ss_plugin,omitempty"`
	SSPluginOpts   string            `json:"ss_plugin_opts,omitempty"`
	SocksVersion   string            `json:"socks_version,omitempty"`
	Network        string            `json:"network,omitempty"`
	ServerPorts    []string          `json:"server_ports,omitempty"`
	ObfsType       string            `json:"obfs_type,omitempty"`
	ObfsPassword   string            `json:"obfs_password,omitempty"`
	AlterID        int               `json:"alter_id,omitempty"`
	Security       string            `json:"security,omitempty"`
	RealityPubKey  string            `json:"reality_public_key,omitempty"`
	RealityShortID string            `json:"reality_short_id,omitempty"`
	Extra          map[string]string `json:"extra,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
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
		Panel: PanelConfig{
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
			GeoIPPath:   "/data/adb/privstack/data/geoip.db",
			GeoSitePath: "/data/adb/privstack/data/geosite.db",
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
		Health: HealthConfig{
			Enabled:     true,
			IntervalSec: 30,
			Threshold:   3,
			URL:         "https://www.gstatic.com/generate_204",
			TimeoutSec:  5,
		},
		Rescue: RescueConfig{
			Enabled:     true,
			MaxAttempts: 3,
			CooldownSec: 60,
		},
		Autostart: true,
	}
}

// DefaultPanelConfig returns the APK-facing metadata defaults stored in panel.json.
func DefaultPanelConfig() PanelConfig {
	return PanelConfig{
		ID:   "default",
		Name: "Default",
	}
}

// ResolvePanelInbounds returns the effective panel inbound settings with
// defaults applied even when the APK-facing panel section is absent.
func (c *Config) ResolvePanelInbounds() PanelInboundsConfig {
	result := PanelInboundsConfig{
		SocksPort: 0,
		HTTPPort:  0,
		AllowLAN:  false,
	}
	if len(c.Panel.Inbounds) == 0 {
		return result
	}
	var decoded PanelInboundsConfig
	if err := json.Unmarshal(c.Panel.Inbounds, &decoded); err != nil {
		return result
	}
	if decoded.SocksPort > 0 {
		result.SocksPort = decoded.SocksPort
	}
	if decoded.HTTPPort > 0 {
		result.HTTPPort = decoded.HTTPPort
	}
	result.AllowLAN = decoded.AllowLAN
	return result
}

// Load reads a Config from the given JSON file path.
// If the file does not exist, it returns DefaultConfig.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			panel, found, panelErr := loadPanelFromSidecar(PanelPath(path))
			if panelErr != nil {
				return nil, panelErr
			}
			cfg.Panel = normalizePanelConfig(panel)
			cfg.SyncFromPanel(found)
			return cfg, nil
		}
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	cfg := DefaultConfig()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// Older module defaults stored `node` as an empty array. Accept that legacy
	// shape and normalize it to the current single-node object form.
	if nodeRaw, ok := raw["node"]; ok {
		trimmed := bytes.TrimSpace(nodeRaw)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			var legacy []NodeConfig
			if err := json.Unmarshal(trimmed, &legacy); err != nil {
				return nil, fmt.Errorf("config: parse legacy node field in %s: %w", path, err)
			}
			normalized := NodeConfig{}
			if len(legacy) > 0 {
				normalized = legacy[0]
			}
			nodeObj, err := json.Marshal(normalized)
			if err != nil {
				return nil, fmt.Errorf("config: normalize legacy node field in %s: %w", path, err)
			}
			raw["node"] = nodeObj
			data, err = json.Marshal(raw)
			if err != nil {
				return nil, fmt.Errorf("config: rebuild normalized config %s: %w", path, err)
			}
		}
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	panelPath := PanelPath(path)
	panel, found, err := loadPanelFromSidecar(panelPath)
	if err != nil {
		return nil, err
	}
	panelAuthoritative := found
	if found {
		cfg.Panel = normalizePanelConfig(panel)
	} else {
		legacyPanel, hasLegacy, err := loadLegacyPanel(raw)
		if err != nil {
			return nil, err
		}
		panelAuthoritative = hasLegacy
		cfg.Panel = normalizePanelConfig(legacyPanel)
		if hasLegacy {
			if err := SavePanel(panelPath, cfg.Panel); err != nil {
				return nil, err
			}
			if err := cfg.Save(path); err != nil {
				return nil, err
			}
		}
	}
	cfg.SyncFromPanel(panelAuthoritative)

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

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// PanelPath returns the sidecar path for APK-facing panel state.
func PanelPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), panelFileName)
}

// SavePanel writes the APK-facing panel metadata atomically.
func SavePanel(path string, panel PanelConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("panel: mkdir: %w", err)
	}

	panel = normalizePanelConfig(panel)
	data, err := json.MarshalIndent(panel, "", "  ")
	if err != nil {
		return fmt.Errorf("panel: marshal: %w", err)
	}
	data = append(data, '\n')

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("panel: write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("panel: rename %s: %w", path, err)
	}
	return nil
}

// Validate checks the Config for obvious misconfigurations.
func (c *Config) Validate() error {
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
		"hysteria2": true, "tuic": true,
	}
	if c.Node.Protocol != "" && !validProto[c.Node.Protocol] {
		return fmt.Errorf("node.protocol must be one of vless/trojan/vmess/shadowsocks/socks/hysteria2/tuic, got %q", c.Node.Protocol)
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
		Protocol:       c.Node.Protocol,
		Address:        c.Node.Address,
		Port:           c.Node.Port,
		UUID:           c.Node.UUID,
		Username:       c.Node.Username,
		Password:       c.Node.Password,
		Flow:           c.Node.Flow,
		Transport:      c.Transport.Protocol,
		TLSServer:      c.Transport.TLSServer,
		Fingerprint:    c.Transport.Fingerprint,
		SSMethod:       c.Node.SSMethod,
		SSPlugin:       c.Node.SSPlugin,
		SSPluginOpts:   c.Node.SSPluginOpts,
		SocksVersion:   c.Node.SocksVersion,
		Network:        c.Node.Network,
		ServerPorts:    c.Node.ServerPorts,
		ObfsType:       c.Node.ObfsType,
		ObfsPassword:   c.Node.ObfsPassword,
		AlterID:        c.Node.AlterID,
		Security:       c.Node.Security,
		RealityPubKey:  c.Node.RealityPublicKey,
		RealityShortID: c.Node.RealityShortID,
		Extra:          c.Transport.Extra,
	}
}

func normalizePanelConfig(panel PanelConfig) PanelConfig {
	if panel.ID == "" {
		panel.ID = DefaultPanelConfig().ID
	}
	if panel.Name == "" {
		panel.Name = DefaultPanelConfig().Name
	}
	if panel.Nodes == nil {
		panel.Nodes = []json.RawMessage{}
	}
	return panel
}

func loadLegacyPanel(raw map[string]json.RawMessage) (PanelConfig, bool, error) {
	panelRaw, ok := raw["panel"]
	if !ok {
		return DefaultPanelConfig(), false, nil
	}

	panel := DefaultPanelConfig()
	if err := json.Unmarshal(panelRaw, &panel); err != nil {
		return PanelConfig{}, false, fmt.Errorf("config: parse legacy panel: %w", err)
	}
	return panel, true, nil
}

func loadPanelFromSidecar(path string) (PanelConfig, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultPanelConfig(), false, nil
		}
		return PanelConfig{}, false, fmt.Errorf("panel: read %s: %w", path, err)
	}

	panel := DefaultPanelConfig()
	if err := json.Unmarshal(data, &panel); err != nil {
		return PanelConfig{}, false, fmt.Errorf("panel: parse %s: %w", path, err)
	}
	return panel, true, nil
}

func (c *Config) SyncFromPanel(authoritative bool) {
	if profile := ResolveActivePanelProfile(c); profile != nil {
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
