# PrivStack --- Implementation Plan

## VPN-Invisible Transparent Proxy: Magisk Module + Android APK Controller

**Based on:** 21 parallel research agents analyzing box4magisk, NetProxy-Magisk, RKNHardering, v2rayNG, Amnezia VPN, Hide-My-Applist, Riru-VpnHide, tproxy internals, Magisk lifecycle, Android API edge cases, and UI/UX patterns.

---

## Executive Summary

PrivStack is a greenfield two-component system for rooted Android devices:
1. **Magisk module** --- transparent proxy via `tproxy + iptables` (no VPN, no TUN, no icon)
2. **Android APK** --- pure controller with zero network permissions (Kotlin + Jetpack Compose)

**Core principle:** sing-box tproxy in whitelist mode. By default ALL traffic goes direct. User explicitly selects which apps route through proxy. Banking apps never see VPN indicators.

**Detection coverage:** Defeats 9 of 15 known detection vectors completely, 4 partially. Remaining 2 are server-side (require residential IP).

---

## Technology Stack

| Component | Language / Framework | Notes |
|-----------|---------------------|-------|
| Module scripts | POSIX sh (busybox compatible) | `iptables.sh`, `dns.sh`, `net_handler.sh`, boot scripts |
| Daemon (`privd`) | Go (statically compiled for arm64) | Root daemon, state machine, health monitor |
| CLI (`privctl`) | Go (same binary or separate) | Thin JSON-RPC client over Unix socket |
| APK | Kotlin, Jetpack Compose, Material 3, Hilt DI | Zero network permissions, pure controller |
| Transport core | sing-box (primary), xray-core (optional) | tproxy inbound, protocol outbounds |
| Build: APK | Gradle (Kotlin DSL) | Standard Android project |
| Build: daemon | `go build` with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` | Static binary, no libc dependency |
| Build: module | `zip` | Magisk module ZIP from assembled directory |

---

## Phase 0: Foundation (Week 1-2)

### 0.1 Repository Setup

```
privstack/
├── module/                    # Magisk module
│   ├── module.prop
│   ├── customize.sh
│   ├── post-fs-data.sh
│   ├── service.sh
│   ├── sepolicy.rule
│   ├── uninstall.sh
│   ├── scripts/
│   │   ├── iptables.sh        # Chain setup/teardown
│   │   ├── routing.sh         # ip rule/route for fwmark
│   │   ├── dns.sh             # DNS interception (TPROXY-based)
│   │   └── net_handler.sh     # Network change handler
│   ├── defaults/
│   │   └── config.json        # Default canonical config
│   └── binaries/
│       └── arm64/             # sing-box, privctl (built separately)
├── daemon/                    # privd source (Go)
│   ├── cmd/privd/
│   ├── cmd/privctl/
│   ├── internal/
│   │   ├── config/            # Config parsing + validation
│   │   ├── core/              # sing-box process management
│   │   ├── netfilter/         # iptables rule generation
│   │   ├── health/            # Health monitoring
│   │   ├── rescue/            # Auto-recovery
│   │   ├── ipc/               # Unix socket JSON-RPC server
│   │   └── watcher/           # inotifyd network change handling
│   └── go.mod
├── app/                       # Android APK (Kotlin + Compose)
│   ├── app/src/main/
│   │   ├── kotlin/com/privstack/panel/
│   │   │   ├── ipc/           # su -c privctl executor
│   │   │   ├── model/         # Kotlin data classes
│   │   │   ├── repository/    # Cache-backed repos
│   │   │   ├── import/        # URI parser, subscription, Amnezia
│   │   │   ├── advisor/       # App classification + placement
│   │   │   └── ui/            # Compose screens
│   │   └── AndroidManifest.xml
│   └── build.gradle.kts
└── docs/
    └── PRIVSTACK_ARCHITECTURE.md
```

### 0.2 Decisions Locked

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Primary core | **sing-box** | Native tproxy, stable FakeDNS, Hysteria2/TUIC support, 12-20MB RAM |
| Secondary core | xray-core (optional) | gRPC hot-swap API; user preference |
| Proxy mode | **Whitelist** (default direct) | Banks never proxied; safest for detection |
| Loop prevention | **GID-based** (`--gid-owner 23333`) | Cleaner than fwmark; no core config dependency |
| IPC protocol | **JSON-RPC 2.0** over Unix socket | Simple, debuggable, newline-delimited |
| APK permissions | **CAMERA only** (optional, for QR) | No INTERNET, no VPN, no ACCESS_NETWORK_STATE |
| Config format | **JSON** (not shell-sourced ini) | Validated, no injection risk |
| Root managers | Magisk + KernelSU + APatch | Detect via env vars, adapt busybox path |

---

## Phase 1: Magisk Module Shell Layer (Week 2-3)

### 1.1 iptables.sh --- Chain Architecture

**Whitelist mode: default = RETURN (direct). Only listed UIDs get proxied.**

```
OUTPUT (mangle)
  └→ PRIVSTACK_OUT
      ├→ GID 23333: RETURN              (loop prevention --- sing-box)
      ├→ mark 0xff: RETURN              (belt-and-suspenders mark check)
      ├→ UID 1073: RETURN               (NetworkStack --- captive portal)
      ├→ PRIVSTACK_BYPASS (reserved IPs) → RETURN
      ├→ PRIVSTACK_APP (whitelist UIDs)
      │     UID 10157: MARK 0x1          (Chrome)
      │     UID 10203: MARK 0x1          (Telegram)
      │     default: RETURN              (everything else → direct)
      └→ (no final MARK --- only whitelisted marked)

PREROUTING (mangle)
  └→ PRIVSTACK_PRE
      ├→ DIVERT (socket match → MARK + ACCEPT)
      ├→ PRIVSTACK_BYPASS (reserved IPs)
      ├→ TPROXY mark 0x1 → port 12345 (TCP+UDP)

OUTPUT (nat) --- DNS only
  └→ PRIVSTACK_DNS
      ├→ GID 23333: RETURN
      ├→ UDP/TCP :53 → REDIRECT :1053    (ALL DNS through sing-box)
```

**Policy routing (IPv4 + IPv6):**
```sh
# IPv4
ip rule add fwmark 0x1 table 100 pref 100
ip route add local 0.0.0.0/0 dev lo table 100

# IPv6 — MUST mirror. IPv6 is critical for RKN bypass
# (blocked IPv4 resources often reachable via IPv6)
ip -6 rule add fwmark 0x1 table 106 pref 100
ip -6 route add local ::/0 dev lo table 106
```

**IPv6 rules — full mirror of IPv4 (DO NOT disable IPv6):**

IPv6 is essential in Russia — many resources blocked by IPv4 address are still
reachable via IPv6. Every iptables rule MUST have an ip6tables counterpart.

```sh
# Mirror ALL chains in ip6tables:
ip6tables -t mangle -N PRIVSTACK_OUT
ip6tables -t mangle -N PRIVSTACK_PRE
ip6tables -t mangle -N PRIVSTACK_BYPASS
ip6tables -t mangle -N PRIVSTACK_APP
ip6tables -t mangle -N DIVERT

# PRIVSTACK_BYPASS for IPv6 reserved ranges:
for cidr in ::1/128 fe80::/10 fc00::/7 ff00::/8; do
  ip6tables -w 100 -t mangle -A PRIVSTACK_BYPASS -d $cidr -j RETURN
done

# Same GID/UID/MARK rules, same APP whitelist, same TPROXY target
# sing-box must listen on [::1]:12345 in addition to 127.0.0.1:12345
# or on [::]:12345 (dual-stack)

# API protection — MUST also be in ip6tables:
ip6tables -A OUTPUT -o lo -p tcp --dport $API_PORT -m owner --uid-owner 0 -j ACCEPT
ip6tables -A OUTPUT -o lo -p tcp --dport $API_PORT -j REJECT
```

**ICMP/ICMPv6 handling (prevent black hole):**
```sh
# ICMP from whitelisted apps would get MARKed but no TPROXY handler exists
# for ICMP → routing loop until TTL=0. Fix: let ICMP go direct.
iptables -t mangle -A PRIVSTACK_OUT -p icmp -j RETURN
ip6tables -t mangle -A PRIVSTACK_OUT -p icmpv6 -j RETURN
```

**Key: `iptables -w 100` and `ip6tables -w 100`** on all commands (MIUI compat, concurrent access).

**Atomic rule application via iptables-restore:**
```sh
# Generate complete ruleset, apply atomically (no partial-apply risk):
iptables-save > /data/adb/privstack/backup/iptables_pre.rules
ip6tables-save > /data/adb/privstack/backup/ip6tables_pre.rules
iptables-restore < /data/adb/privstack/run/rules.v4
ip6tables-restore < /data/adb/privstack/run/rules.v6
```

**Anti-loopback (IPv4 + IPv6):**
```sh
iptables -A OUTPUT -d 127.0.0.1 -p tcp -m owner --gid-owner 23333 \
  -m tcp --dport 12345 -j REJECT
ip6tables -A OUTPUT -d ::1 -p tcp -m owner --gid-owner 23333 \
  -m tcp --dport 12345 -j REJECT
```

**API port protection (RKNHardering vector):**
```sh
iptables -A OUTPUT -o lo -p tcp --dport 9090 -m owner --uid-owner 0 -j ACCEPT
iptables -A OUTPUT -o lo -p tcp --dport 9090 -j REJECT
```

### 1.2 net_handler.sh --- Network Change

- Monitor: `inotifyd` on `/data/misc/net/:w`
- Debounce: 2-second lock file
- Actions:
  1. Flush + rebuild `PRIVSTACK_BYPASS` with current local IPs
  2. Verify `ip rule` fwmark entry -> re-add if missing (airplane mode fix)
  3. Verify `PREROUTING -j PRIVSTACK_PRE` -> re-add if missing
  4. Detect new tethering interfaces -> add TPROXY rules

### 1.3 dns.sh --- DNS Interception

**DO NOT disable Private DNS.** Apps can detect Private DNS state via `Settings.Global.getString("private_dns_mode")`. Instead, use TPROXY to transparently intercept DoT and DoH traffic.

```sh
# DO NOT disable Private DNS --- apps can detect it via Settings.Global!
# Instead, TPROXY transparently intercepts both plain DNS (53) and DoT (853).
# sing-box handles DNS resolution internally via DoH through the proxy tunnel.

# Redirect plain DNS (port 53) to sing-box DNS inbound
iptables -t nat -A PRIVSTACK_DNS -p udp --dport 53 \
  -m owner ! --gid-owner 23333 -j REDIRECT --to-ports 1053
iptables -t nat -A PRIVSTACK_DNS -p tcp --dport 53 \
  -m owner ! --gid-owner 23333 -j REDIRECT --to-ports 1053

# DoT (port 853) goes through TPROXY naturally as TCP traffic
# DoH (port 443) goes through TPROXY naturally as HTTPS traffic
# Chrome's built-in DoH also captured by TPROXY (it's just HTTPS to dns.google)
```

### 1.4 post-fs-data.sh

- Create `/data/adb/privstack/` skeleton
- Set permissions (binaries 0750, config 0600)
- Enable `ip_forward`, disable `rp_filter`
- Verify binaries exist

### 1.5 service.sh

- Detect root manager (Magisk/KSU/APatch)
- Wait `sys.boot_completed` (120s timeout) + 5s settle
- Set `ulimit -SHn 1000000`
- Launch `privd` via `nohup setsid`
- Set `oom_score_adj=-17` on daemon PID (phantom process kill protection)

### 1.6 customize.sh

- Validate arch=arm64, API>=28
- Check TPROXY kernel support (`/proc/config.gz`)
- Preserve existing config on upgrade
- Copy binaries, set capabilities (`cap_net_admin,cap_net_raw+ep`)

---

## Phase 2: Daemon (privd) in Go (Week 3-5)

### 2.1 State Machine

```
STOPPED → STARTING → RUNNING → DEGRADED → RESCUE → STOPPED (rollback)
              ↑                     ↑         |
              └─────── stop() ──────┘    ok → RUNNING
```

### 2.2 Start Sequence

```
1. Read + validate config.json
2. Render sing-box config from template + node
3. Spawn sing-box (uid=0, gid=23333) → wait for listen (30s)
4. Backup iptables: iptables-save > backup/
5. Run scripts/iptables.sh start
6. Run scripts/dns.sh start
7. Run initial health check
8. Start health loop (30s interval)
9. Start inotifyd watchers
→ State: RUNNING
```

### 2.3 Stop Sequence (ORDER MATTERS)

```
1. scripts/dns.sh stop
2. scripts/iptables.sh stop (BEFORE killing sing-box!)
3. SIGTERM → sing-box, wait 5s, SIGKILL if needed
4. Clean PIDs, state files
→ State: STOPPED
```

### 2.4 Health Monitor

| Check | Method | Failure -> |
|-------|--------|-----------|
| sing-box alive | `kill -0 $PID` | Rescue: restart core |
| Port listening | TCP connect :12345 | Rescue: restart core |
| iptables intact | `iptables -C PREROUTING -j PRIVSTACK_PRE` | Rescue: re-apply rules |
| Routing intact | `ip rule show \| grep fwmark` | Rescue: re-add rule |
| DNS working | Resolve `example.com` | Warning only |

3 consecutive failures -> DEGRADED -> RESCUE (3 attempts) -> STOPPED + rollback.

### 2.5 Hot-Swap (Node Switch)

```
1. Render new sing-box config
2. SIGTERM → sing-box
3. Spawn new sing-box
4. Wait for listen
→ iptables untouched, zero network gap for non-proxied apps
```

### 2.6 IPC Server

Unix socket at `/data/adb/privstack/run/daemon.sock`.
Protocol: JSON-RPC 2.0, newline-delimited.

**20 methods:** status, start, stop, reload, health, audit, config-get/set/list/delete, config-import, config-export, logs, logs-stream, resolve-uid, app-list, routing-set, version.

### 2.7 sing-box Config Generation

The daemon renders sing-box JSON configs from the canonical config. Example generated output:

```json
{
  "log": {"level": "warn"},
  "dns": {
    "servers": [
      {"tag": "proxy-dns", "address": "https://1.1.1.1/dns-query", "detour": "proxy"},
      {"tag": "direct-dns", "address": "local"}
    ],
    "rules": [{"rule_set": ["geosite-category-ads-all"], "server": "block"}]
  },
  "inbounds": [
    {"type": "tproxy", "tag": "tproxy-in", "listen": "::", "listen_port": 12345,
     "sniff": true, "sniff_override_destination": false},
    {"type": "direct", "tag": "dns-in", "listen": "::", "listen_port": 1053,
     "override_address": "1.1.1.1", "override_port": 53}
  ],
  "outbounds": [
    {"type": "vless", "tag": "proxy", "server": "...", "server_port": 443,
     "uuid": "...", "flow": "xtls-rprx-vision",
     "tls": {"enabled": true, "server_name": "...",
       "reality": {"enabled": true, "public_key": "...", "short_id": "..."},
       "utls": {"enabled": true, "fingerprint": "chrome"}}},
    {"type": "direct", "tag": "direct"},
    {"type": "block", "tag": "block"},
    {"type": "dns", "tag": "dns-out"}
  ],
  "route": {
    "auto_detect_interface": true,
    "default_mark": 255,
    "rules": [
      {"protocol": "dns", "outbound": "dns-out"},
      {"ip_is_private": true, "outbound": "direct"}
    ]
  }
}
```

### 2.8 Config Rendering (Go)

The daemon's `internal/config` package handles template rendering:

```go
// internal/config/renderer.go

type ConfigRenderer struct {
    templateDir string
}

// RenderSingBox generates a sing-box JSON config from the canonical config
// and the active node profile.
func (r *ConfigRenderer) RenderSingBox(cfg *CanonicalConfig, node *Node) ([]byte, error) {
    tmpl, err := os.ReadFile(filepath.Join(r.templateDir, "singbox_tproxy.json.tmpl"))
    if err != nil {
        return nil, fmt.Errorf("read template: %w", err)
    }

    t, err := template.New("singbox").Parse(string(tmpl))
    if err != nil {
        return nil, fmt.Errorf("parse template: %w", err)
    }

    data := templateData{
        ListenPort: cfg.Proxy.Port,
        DNSPort:    cfg.Proxy.DNSPort,
        LogLevel:   cfg.Transport.LogLevel,
        Node:       node,
    }

    var buf bytes.Buffer
    if err := t.Execute(&buf, data); err != nil {
        return nil, fmt.Errorf("execute template: %w", err)
    }

    // Validate generated JSON
    var check json.RawMessage
    if err := json.Unmarshal(buf.Bytes(), &check); err != nil {
        return nil, fmt.Errorf("invalid generated config: %w", err)
    }

    return buf.Bytes(), nil
}
```

---

## Phase 3: APK Controller (Week 5-8)

### 3.1 Package: `com.privstack.panel`

**Manifest:**
```xml
<!-- NO INTERNET. NO VPN. NO ACCESS_NETWORK_STATE. -->
<uses-permission android:name="android.permission.CAMERA" />
```

### 3.2 IPC Layer

```kotlin
// ipc/PrivctlExecutor.kt

class PrivctlExecutor @Inject constructor() {
    suspend fun execute(method: String, params: JsonObject = JsonObject(emptyMap())): PrivctlResult =
        withContext(Dispatchers.IO) {
            val json = buildJsonObject {
                put("method", method)
                put("params", params)
            }.toString()
            val process = ProcessBuilder(
                "su", "-c",
                "/data/adb/privstack/bin/privctl $method '$json'"
            ).start()
            val stdout = process.inputStream.bufferedReader().readText()
            val exitCode = process.waitFor()
            when {
                exitCode == 0 -> PrivctlResult.Success(Json.parseToJsonElement(stdout))
                exitCode == 1 -> PrivctlResult.Error(stdout)
                else -> PrivctlResult.RootDenied
            }
        }
}

sealed class PrivctlResult {
    data class Success(val data: JsonElement) : PrivctlResult()
    data class Error(val message: String) : PrivctlResult()
    data object RootDenied : PrivctlResult()
    data object Timeout : PrivctlResult()
}
```

### 3.3 Data Models

```kotlin
// model/Node.kt

@Serializable
data class Node(
    val name: String,
    val protocol: Protocol,
    val address: String,
    val port: Int,
    val uuid: String? = null,
    val password: String? = null,
    val flow: String? = null,
    val security: Security = Security.NONE,
    val sni: String? = null,
    val fingerprint: String = "chrome",
    val publicKey: String? = null,
    val shortId: String? = null,
    val method: String? = null, // Shadowsocks cipher
    val alpn: List<String>? = null,
    val transport: TransportConfig? = null,
)

@Serializable
enum class Protocol { VLESS, VMESS, TROJAN, SHADOWSOCKS, HYSTERIA2, TUIC }

@Serializable
enum class Security { NONE, TLS, REALITY }

@Serializable
data class TransportConfig(
    val type: String = "tcp",  // tcp, ws, grpc, h2
    val path: String? = null,
    val host: String? = null,
    val serviceName: String? = null,
)
```

```kotlin
// model/DaemonStatus.kt

@Serializable
data class DaemonStatus(
    val state: DaemonState,
    val uptimeSec: Long = 0,
    val server: String? = null,
    val egressIp: String? = null,
    val txBytes: Long = 0,
    val rxBytes: Long = 0,
    val latencyMs: Int? = null,
    val dnsOk: Boolean = false,
    val activeProfile: String? = null,
    val corePid: Int? = null,
    val iptablesActive: Boolean = false,
)

@Serializable
enum class DaemonState { STOPPED, STARTING, RUNNING, DEGRADED, RESCUE, STOPPING }
```

### 3.4 Navigation (4 tabs)

| Tab | Screen | Key Features |
|-----|--------|-------------|
| **Dashboard** | Connection state, traffic sparkline, egress IP, latency, DNS | Pulsing ring animation, connect button |
| **Nodes** | Server list by subscription groups, import, QR scan | TabRow, latency badges, long-press menu |
| **Apps** | Whitelist app picker with templates | Checkbox list, search, template chips, badge count |
| **Settings** | Routing mode, DNS, advanced, module info | SegmentedButtonRow, switches, version |

### 3.5 Dashboard Screen

```kotlin
// ui/dashboard/DashboardScreen.kt

@Composable
fun DashboardScreen(viewModel: DashboardViewModel = hiltViewModel()) {
    val status by viewModel.status.collectAsStateWithLifecycle()
    val traffic by viewModel.trafficHistory.collectAsStateWithLifecycle()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(16.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        ConnectionRing(
            state = status.state,
            onClick = { viewModel.toggleConnection() },
        )
        Spacer(modifier = Modifier.height(24.dp))
        StatusCard(status)
        Spacer(modifier = Modifier.height(16.dp))
        TrafficSparkline(
            data = traffic,
            modifier = Modifier
                .fillMaxWidth()
                .height(120.dp),
        )
        Spacer(modifier = Modifier.height(16.dp))
        MetricsRow(
            latency = status.latencyMs,
            egressIp = status.egressIp,
            dnsOk = status.dnsOk,
        )
    }
}
```

### 3.6 Whitelist App Picker

```
Banner: "Selected apps route through proxy. Everything else connects directly."

Templates: [Browsers] [Social] [Streaming] [All except banks]

Search: [________________________]
[ ] Show system apps

[x] Chrome                    com.android.chrome
[x] Telegram                  org.telegram.messenger
[ ] Sberbank                  ru.sberbankmobile
[ ] YouTube                   com.google.android.youtube
...

[Apply --- 7 apps selected]
```

### 3.7 Import Flow

```
Auto-detect format:
  vpn://     → Amnezia (qCompress + base64url + zlib)
  vless://   → VLESS URI
  vmess://   → VMess base64 JSON
  trojan://  → Trojan URI
  ss://      → Shadowsocks SIP002
  proxies:   → Clash YAML (Meta)
  [json]     → v2rayNG backup / sing-box config
  base64     → Subscription body

Quick Add: paste → parse → validate → preview → confirm → activate
```

### 3.8 Link Parser (Kotlin)

```kotlin
// import/LinkParser.kt

object LinkParser {
    fun parse(uri: String): Result<Node> = runCatching {
        val trimmed = uri.trim()
        when {
            trimmed.startsWith("vless://") -> parseVless(trimmed)
            trimmed.startsWith("vmess://") -> parseVmess(trimmed)
            trimmed.startsWith("trojan://") -> parseTrojan(trimmed)
            trimmed.startsWith("ss://") -> parseShadowsocks(trimmed)
            trimmed.startsWith("hysteria2://") || trimmed.startsWith("hy2://") -> parseHysteria2(trimmed)
            trimmed.startsWith("tuic://") -> parseTuic(trimmed)
            trimmed.startsWith("vpn://") -> parseAmnezia(trimmed)
            else -> error("Unsupported URI scheme")
        }
    }

    private fun parseVless(uri: String): Node {
        // vless://uuid@host:port?security=reality&sni=...&fp=chrome&pbk=...&sid=...#name
        val parsed = Uri.parse(uri)
        val params = parsed.queryParameterNames.associateWith { parsed.getQueryParameter(it) }
        return Node(
            name = parsed.fragment ?: "${parsed.host}:${parsed.port}",
            protocol = Protocol.VLESS,
            address = parsed.host ?: error("missing host"),
            port = parsed.port.takeIf { it > 0 } ?: error("missing port"),
            uuid = parsed.userInfo ?: error("missing uuid"),
            flow = params["flow"],
            security = when (params["security"]) {
                "reality" -> Security.REALITY
                "tls" -> Security.TLS
                else -> Security.NONE
            },
            sni = params["sni"],
            fingerprint = params["fp"] ?: "chrome",
            publicKey = params["pbk"],
            shortId = params["sid"],
            transport = params["type"]?.let { type ->
                TransportConfig(
                    type = type,
                    path = params["path"],
                    host = params["host"],
                    serviceName = params["serviceName"],
                )
            },
        )
    }

    // parseTrojan, parseVmess, parseShadowsocks, etc. follow same pattern
}
```

### 3.9 Audit Module (14 checks)

| Check | Severity | What |
|-------|----------|------|
| `vpn_api_surface` | CRITICAL | No TRANSPORT_VPN visible |
| `tun_interface` | HIGH | No tun0/wg0 |
| `not_vpn_capability` | CRITICAL | NET_CAPABILITY_NOT_VPN present |
| `core_api_protected` | MEDIUM | API port restricted to UID 0 |
| `dns_leak` | MEDIUM | DNS through proxy |
| `package_visibility` | HIGH | Proxy apps not visible to risk apps |
| `iptables_loop_safe` | HIGH | GID exemption in OUTPUT |
| `proc_net_leak` | HIGH | Proxy port not in /proc/net/tcp |

---

## Phase 4: Protocol Support (Week 8-10)

### 4.1 Primary: sing-box Protocols

| Protocol | Status | sing-box type |
|----------|--------|---------------|
| VLESS + Reality | Primary | `vless` + `tls.reality` |
| VLESS + TLS | Supported | `vless` + `tls` |
| Trojan | Supported | `trojan` |
| VMess | Supported | `vmess` |
| Shadowsocks 2022 | Supported | `shadowsocks` |
| Hysteria2 | Supported | `hysteria2` |
| TUIC v5 | Supported | `tuic` |

### 4.2 Secondary: AmneziaWG (via wireproxy-awg)

```
iptables TPROXY → sing-box tproxy-in → wireproxy-awg SOCKS5 → AWG server
```

- wireproxy-awg runs as separate process (gVisor netstack, no kernel interface)
- TCP only through SOCKS5 (UDP ASSOCIATE not implemented)
- DNS via static UDPProxyTunnel
- Full AWG obfuscation: Jc, S1-S4, H1-H4, I1-I5

### 4.3 NOT Supported (by design)

- Standard WireGuard via TUN (creates visible interface)
- OpenVPN (not a proxy protocol)
- Any protocol requiring VpnService

---

## Phase 5: Isolation Layer (Week 10-11)

### 5.1 Work Profile (Primary)

APK's Placement Advisor recommends profile separation:

| Category | Profile | Examples |
|----------|---------|----------|
| Banking | Work | Sberbank, T-Bank, VTB, Alfa |
| Government | Work | Gosuslugi, Nalog |
| Telecom | Work | MTS, Megafon, Beeline |
| Marketplace | Work | Samokat, MegaMarket, Ozon |
| Proxy tools | Personal | PrivStack, Terminal |
| Browsers | Personal | Chrome (proxied via whitelist) |

### 5.2 Hide-My-Applist (Fallback)

If Work Profile not feasible, HMA via LSPosed:
- Single hook: `shouldFilterApplication` in system_server
- Hides VPN/proxy packages from aggressive apps
- Limitation: requires Xposed (itself detectable)

---

## Phase 6: Testing & Hardening (Week 11-12)

### 6.1 Detection Test Matrix

Run audit against each vector with module active:

| # | Test | Expected | Tool |
|---|------|----------|------|
| 1 | `NetworkCapabilities.TRANSPORT_VPN` | Not set | RKNHardering |
| 2 | `NetworkInterface.getNetworkInterfaces()` | Only wlan0/rmnet | VPN-Detector |
| 3 | VPN icon in statusbar | Not shown | Visual |
| 4 | `NOT_VPN` capability | Present | RKNHardering |
| 5 | `/proc/net/tcp` scan | Proxy port not visible (Android 10+) | RKNHardering |
| 6 | Package enumeration | Proxy apps hidden (Work Profile) | Sberbank test |
| 7 | sing-box API port | Rejected for non-root | netcat test |
| 8 | DNS leak | All DNS through proxy | dnsleaktest.com |
| 9 | Airplane mode toggle | Rules survive/re-applied | Manual |
| 10 | WiFi <-> mobile switch | Bypass IPs updated, no leak | Manual |
| 11 | MIUI iptables flush | Rules auto-restored (30s check) | MIUI device |

### 6.2 ROM Compatibility

| ROM | Known Issues | Workaround |
|-----|-------------|------------|
| MIUI/HyperOS | Periodic iptables flush | Health monitor re-applies rules |
| Samsung OneUI | KNOX strict SELinux | Test sepolicy.rule, no `permissive` |
| OnePlus ColorOS | iptables-nft vs legacy | Detect and use `iptables-legacy` if available |
| Huawei EMUI | May lack TPROXY kernel | Auto-fallback to REDIRECT (TCP only) |
| AOSP/Lineage | Standard behavior | No workarounds needed |

### 6.3 Edge Cases

- [ ] App install after rules active (whitelist: safe, goes direct)
- [ ] Shared UID apps (warn user in UI)
- [ ] Multi-user / Work Profile UIDs (`user * 100000 + appid`)
- [ ] VPN coexistence (detect tun0, warn user, bypass VPN UID)
- [ ] Captive portal on hotel WiFi (UID 1073 bypassed)
- [ ] Full IPv6 support via mirrored ip6tables (critical for RKN bypass where IPv4 is blocked)
- [ ] IPv6-only network with NAT64 (sing-box handles dual-stack natively)
- [ ] sing-box tproxy listen on dual-stack `[::]:12345` (covers both IPv4 and IPv6)
- [ ] Phantom process killing Android 12+ (`oom_score_adj=-17`, `setsid`)

---

## Deliverables Summary

| Component | Language | Size Est. | Priority |
|-----------|----------|-----------|----------|
| `scripts/iptables.sh` | Shell | ~500 lines | P0 --- core |
| `scripts/dns.sh` | Shell | ~80 lines | P0 --- core |
| `scripts/net_handler.sh` | Shell | ~150 lines | P0 --- stability |
| `module/*.sh` (boot scripts) | Shell | ~200 lines | P0 --- core |
| `privd` daemon | Go | ~2000 lines | P0 --- core |
| `privctl` CLI | Go | ~300 lines | P0 --- core |
| APK IPC layer | Kotlin | ~400 lines | P1 --- UI |
| APK Dashboard | Compose | ~600 lines | P1 --- UI |
| APK Node management | Compose | ~800 lines | P1 --- UI |
| APK App picker | Compose | ~500 lines | P1 --- UI |
| APK Import/Export | Kotlin | ~700 lines | P1 --- UI |
| APK Audit module | Kotlin | ~400 lines | P2 --- security |
| APK Placement Advisor | Kotlin | ~300 lines | P2 --- security |
| wireproxy-awg integration | Shell/Config | ~200 lines | P3 --- AWG |

**Total estimated: ~7000 lines of code.**

---

## Risk Register

| Risk | Impact | Mitigation |
|------|--------|------------|
| TPROXY not in kernel | No UDP proxy | Auto-detect, fallback to REDIRECT |
| Mintsifry deadline Apr 15 | Banks enforce detection | tproxy defeats all on-device vectors |
| MIUI flushes iptables | Traffic leak | 30s health check + inotifyd repair |
| Phantom process killing | Daemon dies | oom_score_adj=-17 + setsid + watchdog |
| SELinux denials | Module fails on strict ROMs | Explicit sepolicy.rule, not `permissive` |
| sing-box crash | Traffic blackhole (TPROXY to dead port) | Health monitor -> rescue in <30s |
| Datacenter IP detected | Server-side check, unavoidable | Document: use residential exit IP |
| IPv6 rules out of sync | IPv6 traffic bypasses proxy | Mirror ALL iptables rules in ip6tables; test with `curl -6` |
| ICMP black hole | Whitelisted app ping loops | `-p icmp -j RETURN` before APP chain |
| Partial iptables apply | Black hole on power loss | Use `iptables-restore` / `ip6tables-restore` for atomicity |
