# PrivStack: Complete Architecture

## Transparent Proxy Module (Magisk) + Controller APK for VPN-Invisible Proxying

**Version:** 1.0 draft  
**Date:** 2026-04-11  
**Status:** Architecture design, pre-implementation

---

## Table of Contents

1. [Threat Model](#1-threat-model)
2. [Architecture Overview](#2-architecture-overview)
3. [Detection Matrix](#3-detection-matrix)
4. [Magisk Module: privstack](#4-magisk-module-privstack)
5. [Root Daemon: privd](#5-root-daemon-privd)
6. [CLI Client: privctl](#6-cli-client-privctl)
7. [Transport Core: xray-core tproxy](#7-transport-core-xray-core-tproxy)
8. [iptables Chain Architecture](#8-iptables-chain-architecture)
9. [APK Controller: PrivStack Panel](#9-apk-controller-privstack-panel)
10. [IPC Protocol](#10-ipc-protocol)
11. [Isolation Layer](#11-isolation-layer)
12. [Audit System](#12-audit-system)
13. [Amnezia VPN + VLESS Hiding Plan](#13-amnezia-vpn--vless-hiding-plan)
14. [Boot Sequence](#14-boot-sequence)
15. [Canonical Config Schema](#15-canonical-config-schema)
16. [File Layout](#16-file-layout)

---

## 1. Threat Model

### 1.1 Context

Russia's Digital Ministry has distributed an official VPN detection guide to companies (deadline: April 15, 2026). Research by RKS Global confirms:

- **22 of 30** most popular Android apps detect VPN usage
- **19 of 30** transmit VPN status to remote servers
- **24 of 30** send lists of installed packages to servers
- **27 of 30** have `QUERY_ALL_PACKAGES` or specific `<queries>` declarations

### 1.2 Detection Vectors (from RKNHardering + RKS Research)

The RKNHardering project implements the exact methodology used for detection. Six parallel checkers:

| # | Vector | API / Method | Severity |
|---|--------|-------------|----------|
| 1 | **TRANSPORT_VPN flag** | `NetworkCapabilities.hasTransport(TRANSPORT_VPN)` | CRITICAL |
| 2 | **TUN interface** | `NetworkInterface.getNetworkInterfaces()`, `/proc/net/if_inet6` | CRITICAL |
| 3 | **VPN icon in statusbar** | OS-enforced when VpnService active | HIGH |
| 4 | **NOT_VPN absent** | `!hasCapability(NET_CAPABILITY_NOT_VPN)` | CRITICAL |
| 5 | **Package enumeration** | `PackageManager.getInstalledPackages()` — 23 known VPN packages | HIGH |
| 6 | **VpnService intent query** | `queryIntentServices("android.net.VpnService")` | HIGH |
| 7 | **Localhost port scan** | `/proc/net/tcp` — known proxy ports (1080, 10808, 7890...) | MEDIUM |
| 8 | **Xray gRPC API scan** | gRPC handshake on all 64K localhost ports | MEDIUM |
| 9 | **DNS anomaly** | Loopback DNS, DNS mismatch with underlying network | MEDIUM |
| 10 | **GeoIP + geolocation** | IP country vs cell tower/SIM MCC correlation | HIGH |
| 11 | **dumpsys vpn_management** | Active VPN entries (Android 12+) | HIGH |
| 12 | **dumpsys VpnService** | Running VpnService instances | HIGH |
| 13 | **MTU anomaly** | VPN interfaces typically MTU < 1500 | LOW |
| 14 | **Underlying network probe** | Bind to non-VPN network, check direct egress | MEDIUM |
| 15 | **Parallel request** | Russian domain + foreign domain timing comparison | MEDIUM |

### 1.3 Ministry Three-Stage Protocol

1. Compare device IP against Russian/blocked address databases
2. Check for VPN through company's own app on the device
3. Parallel requests to Russian + foreign domain (split-tunnel detection)

---

## 2. Architecture Overview

```
+----------------------------------------------------------+
|                    ANDROID DEVICE (rooted)                |
|                                                          |
|  +-----------------+        +-------------------------+  |
|  | APK Controller  |        | Magisk Module: privstack|  |
|  | (no INTERNET    |  su -c | +---------------------+ |  |
|  |  no VPN perms)  |------->| | privd (root daemon) | |  |
|  |                 | privctl| |   state machine      | |  |
|  | com.privstack   |        | |   health monitor     | |  |
|  | .panel          |        | |   rescue/rollback    | |  |
|  +-----------------+        | +----------+-----------+ |  |
|                             |            |             |  |
|                             | +----------v-----------+ |  |
|                             | | xray-core            | |  |
|                             | | dokodemo-door tproxy | |  |
|                             | | VLESS-Reality out    | |  |
|                             | +----------+-----------+ |  |
|                             |            |             |  |
|                             | +----------v-----------+ |  |
|                             | | iptables (mangle)    | |  |
|                             | | TPROXY + policy      | |  |
|                             | | routing + DNS hijack  | |  |
|                             | +----------------------+ |  |
|                             +-------------------------+  |
|                                                          |
|  +------------------+    +-----------------------------+ |
|  | Work Profile     |    | Personal Profile            | |
|  | (isolated)       |    | (has proxy tools)           | |
|  | Banks, Telecom,  |    | Browser, Telegram,          | |
|  | Marketplaces     |    | privstack APK               | |
|  +------------------+    +-----------------------------+ |
+----------------------------------------------------------+
         |                              |
         | direct (banks see           | tproxy -> xray -> 
         | no VPN, no proxy)           | VLESS-Reality
         v                              v
    [Russian services]           [Proxy server]
```

### Key Principles

1. **No VpnService, no TUN, no VPN icon** — tproxy+iptables operates at netfilter level
2. **APK is just a controller** — no INTERNET permission, no VPN permissions
3. **Separation by profile** — aggressive apps in Work Profile, tools in Personal
4. **Audit, don't hide** — verify what's visible rather than hooking app processes
5. **Rescue on failure** — automatic rollback prevents broken network state

---

## 3. Detection Matrix

How tproxy+iptables handles each vector:

| # | Vector | tproxy Defeats? | Additional Measures |
|---|--------|:---------------:|---------------------|
| 1 | TRANSPORT_VPN | **YES** | — |
| 2 | TUN interface (tun0) | **YES** | — |
| 3 | VPN status bar icon | **YES** | — |
| 4 | NOT_VPN absent | **YES** | — |
| 5 | Package enumeration | **PARTIAL** | Work Profile isolation or Hide-My-Applist |
| 6 | VpnService intent | **YES** | — |
| 7 | Localhost port scan | **PARTIAL** | Non-standard port + SELinux (Android 10+) |
| 8 | Xray gRPC API | **NO** | iptables: restrict API port to UID 0 only |
| 9 | DNS anomaly | **PARTIAL** | Route ALL DNS through proxy, disable Private DNS |
| 10 | GeoIP + geolocation | **NO** | Residential exit IP; unavoidable for foreign IP |
| 11 | dumpsys vpn_management | **YES** | — |
| 12 | dumpsys VpnService | **YES** | — |
| 13 | MTU anomaly | **YES** | — |
| 14 | Underlying network probe | **YES** | No separate VPN network to bypass |
| 15 | Parallel request | **PARTIAL** | Use global proxy mode (no split-routing) |

**Result: 9 of 15 vectors fully defeated, 4 partially, 2 require infrastructure changes.**

---

## 4. Magisk Module: privstack

### 4.1 Module Directory

```
/data/adb/modules/privstack/
    module.prop              # id=privstack, version, description
    post-fs-data.sh          # Early init: dirs, permissions, ip_forward
    service.sh               # Boot launcher: waits for boot, starts privd
    customize.sh             # Install: validate arch/API, copy binaries, set perms
    sepolicy.rule            # SELinux: net_admin, sockets, proc_net, packages_list
    uninstall.sh             # Cleanup: stop daemon, flush iptables, remove rules
```

### 4.2 Data Directory

```
/data/adb/privstack/
    bin/
        privd                # Root daemon (Go/Rust, arm64, static)
        privctl              # CLI client
        xray                 # xray-core binary
    config/
        config.json          # Canonical config (user-facing)
        rendered/            # Generated runtime xray configs
            xray.json
    profiles/                # Named server profiles
        <name>/config.json
    scripts/
        iptables.sh          # Chain setup/teardown (~500 lines)
        routing.sh           # ip rule + ip route for fwmark
        dns.sh               # DNS hijack + Private DNS toggle
    run/
        daemon.sock          # Unix domain socket (IPC)
        daemon.pid           # privd PID
        xray.pid             # xray PID
        state.json           # Current state snapshot
        health.json          # Latest health check
        runtime_iptables.conf # Params snapshot for clean teardown
    logs/
        privd.log            # Daemon log (rotated)
        xray_error.log       # xray stderr
    backup/
        iptables_pre.rules   # Pre-modification iptables dump
        dns_pre.conf         # Original Private DNS setting
    templates/
        xray_tproxy.json.tmpl
```

### 4.3 module.prop

```properties
id=privstack
name=PrivStack
version=v1.0.0
versionCode=100
author=privstack
description=Transparent proxy via tproxy+iptables. No VPN, no TUN, no icon.
```

### 4.4 post-fs-data.sh (Early Init)

Runs **blocking** at post-fs-data stage, before zygote. Must complete in <10 seconds.

- Creates `/data/adb/privstack/` directory skeleton
- Sets file permissions (binaries: 0750, config: 0600, scripts: 0755)
- Enables `ip_forward`: `echo 1 > /proc/sys/net/ipv4/ip_forward`
- Verifies critical binaries exist (privd, xray)
- Does NOT start daemon (no network at this stage)

### 4.5 service.sh (Daemon Launcher)

Runs **non-blocking** at late_start service stage. Network is available.

```
1. Check manual flag → skip if /data/adb/privstack/config/manual exists
2. Poll sys.boot_completed (up to 120s)
3. Sleep 5s for network interface settle
4. Kill stale daemon/socket
5. Set ulimit -SHn 1000000
6. Launch: nohup privd --config ... --data-dir ... &
7. Verify daemon PID alive after 2s
```

### 4.6 customize.sh (Installation)

- Validates architecture: arm64 only
- Validates API level: Android 9+ (API 28)
- Checks kernel TPROXY support via `/proc/config.gz` → warns if missing
- Preserves existing `config.json` on upgrade (backs up first)
- Copies binaries from ZIP to `/data/adb/privstack/bin/`
- Sets capabilities: `cap_net_admin,cap_net_raw,cap_net_bind_service+ep`

### 4.7 sepolicy.rule

Key grants for magisk context:
- `tcp_socket`, `udp_socket`, `rawip_socket` — full lifecycle
- `unix_stream_socket`, `unix_dgram_socket` — IPC
- `netlink_route_socket`, `netlink_netfilter_socket` — iptables/routing
- `capability net_admin net_raw net_bind_service` — network control
- `proc_net file read` — health checks
- `packages_list_file file read` — UID resolution for per-app filtering

### 4.8 uninstall.sh

1. Send SIGTERM to privd, wait 10s, SIGKILL if needed
2. Flush all PRIVSTACK_* chains from mangle and nat tables
3. Remove ip rule/route entries
4. Preserve `/data/adb/privstack/` data directory (log removal path)

---

## 5. Root Daemon: privd

### 5.1 State Machine

```
              +----------+
    +---------| STOPPED  |<---------+
    |         +----------+          |
    | start()       ^               |
    v               | stop()        |
+----------+        |               |
| STARTING |---fail-+               |
+----------+                        |
    | success                       |
    v                               |
+----------+   health_fail    +-----------+
| RUNNING  |----------------->| DEGRADED  |
+----------+                  +-----------+
    ^   |                         |
    |   | stop()           rescue_fail
    |   v                         |
+----------+                 +---------+
| STOPPING |                 | RESCUE  |
+----------+                 +---------+
                                  |
                             ok --+--> RUNNING
                             fail --> STOPPED (rollback)
```

### 5.2 Start Sequence

```
1. Render xray config from template + config.json
2. Spawn xray as child process (uid=0, gid=23333)
3. Poll for xray to listen on PROXY_PORT (up to 30s)
4. Backup current iptables: iptables-save > backup/
5. Run scripts/iptables.sh start
6. Run scripts/dns.sh start
7. Run initial health check
8. Enter RUNNING state, start health loop
```

### 5.3 Stop Sequence (order matters!)

```
1. Remove DNS hijack FIRST (dns.sh stop)
2. Remove iptables chains (iptables.sh stop) — BEFORE killing xray
3. SIGTERM to xray, wait 5s, SIGKILL if needed
4. Clean PID files
5. Enter STOPPED state
```

**Critical:** iptables removal MUST happen before killing xray. Otherwise, traffic gets TPROXY'd to a dead socket and all connectivity breaks.

### 5.4 Hot-Swap (Node Switching)

Restarts xray only, keeps iptables alive:

```
1. Render new xray config
2. SIGTERM to xray
3. Spawn new xray
4. Wait for listen
5. Run health check
```

iptables rules are unaffected — no network interruption for non-proxied traffic.

### 5.5 Health Monitor

Runs every 30s while RUNNING:

| Check | Method | Action on Fail |
|-------|--------|----------------|
| `xray_alive` | `kill -0 $PID` | Rescue: restart xray |
| `xray_listening` | TCP connect to PROXY_PORT | Rescue: restart xray |
| `iptables_intact` | Verify chain existence | Rescue: re-apply iptables |
| `routing_intact` | Check `ip rule` for fwmark | Rescue: re-apply routing |
| `connectivity` | DNS + HTTP probe through proxy | Warning only |

3 consecutive failures → DEGRADED → RESCUE

### 5.6 Rescue Mechanism

3 attempts with escalating strategy:

1. **Restart xray only** (most common fix — xray crashed)
2. **Re-apply iptables** (chains may have been flushed by other tool)
3. **Full stop + start** (complete rebuild)

If all 3 fail: **rollback** — remove all rules, restore `iptables_pre.rules` from backup, enter STOPPED.

**Guarantee:** System never stays in broken state with partial iptables and no proxy.

### 5.7 Loop Prevention (GID-based)

Xray runs as `gid=23333` (custom group). iptables OUTPUT chain:

```bash
iptables -t mangle -A PRIVSTACK_OUT -m owner --gid-owner 23333 -j RETURN
```

This is cleaner than fwmark-based prevention:
- No need for xray to set marks on its own packets
- Works even if xray's `sockopt.mark` config is wrong
- Cannot be bypassed by misconfigured outbound

### 5.8 Xray API Port Protection

RKNHardering scans all 64K ports for gRPC. Protection:

```bash
# Only root can reach xray API
iptables -A OUTPUT -o lo -p tcp --dport 8080 -m owner --uid-owner 0 -j ACCEPT
iptables -A OUTPUT -o lo -p tcp --dport 8080 -j REJECT
```

---

## 6. CLI Client: privctl

Thin binary that sends JSON commands to privd via Unix socket.

### 6.1 Commands

```
privctl status              # Current state, PIDs, uptime, active node
privctl start [--profile X] # STOPPED → RUNNING
privctl stop                # Any → STOPPED  
privctl restart             # Full restart
privctl reload              # Hot-reload config (restart xray, keep iptables)
privctl switch <node>       # Hot-swap node
privctl health              # Latest health check results
privctl audit <checks...>   # Run security audit
privctl config-get [key]    # Read config
privctl config-set <k> <v>  # Write config + reload
privctl config-list         # List profiles
privctl config-import <uri> # Import VLESS/Trojan/SS URI
privctl logs [--follow]     # View/stream logs
privctl rescue              # Manual rescue trigger
privctl rollback            # Emergency: tear down everything
privctl version             # Daemon + xray versions
```

### 6.2 IPC Format

JSON-RPC 2.0 over Unix domain socket, newline-delimited:

```
→ {"jsonrpc":"2.0","id":1,"method":"status","params":{}}
← {"jsonrpc":"2.0","id":1,"result":{"state":"running","uptime_sec":3842,...}}
```

---

## 7. Transport Core: xray-core tproxy

### 7.1 Inbound Configuration

```json
{
  "tag": "tproxy-in",
  "port": 12345,
  "protocol": "dokodemo-door",
  "settings": {
    "network": "tcp,udp",
    "followRedirect": true
  },
  "sniffing": {
    "enabled": true,
    "destOverride": ["http", "tls", "quic"],
    "routeOnly": true
  },
  "streamSettings": {
    "sockopt": {
      "tproxy": "tproxy"
    }
  }
}
```

**Key settings:**
- `followRedirect: true` — read original destination from TPROXY socket metadata
- `tproxy: "tproxy"` — use `IP_TRANSPARENT` socket option, `IP_RECVORIGDSTADDR` for UDP
- `routeOnly: true` — sniff TLS SNI for routing decisions only, don't override destination

### 7.2 Outbound: VLESS-Reality (Recommended)

```json
{
  "tag": "proxy",
  "protocol": "vless",
  "settings": {
    "vnext": [{
      "address": "server.example.com",
      "port": 443,
      "users": [{
        "id": "<uuid>",
        "flow": "xtls-rprx-vision",
        "encryption": "none"
      }]
    }]
  },
  "streamSettings": {
    "network": "tcp",
    "security": "reality",
    "realitySettings": {
      "serverName": "www.example.com",
      "fingerprint": "chrome",
      "publicKey": "<key>",
      "shortId": "<id>"
    }
  }
}
```

**Why VLESS-Reality:** Server impersonates a real website for active probing resistance. Chrome TLS fingerprint passes JA3/JA4 analysis. XTLS-Vision provides zero-copy TLS passthrough.

### 7.3 DNS Configuration

Split DNS with proxy resolution:

```json
{
  "dns": {
    "servers": [
      {
        "address": "https+local://1.1.1.1/dns-query",
        "domains": ["geosite:geolocation-!cn"]
      },
      {
        "address": "223.5.5.5",
        "domains": ["geosite:cn"],
        "expectIPs": ["geoip:cn"]
      }
    ],
    "queryStrategy": "UseIPv4"
  }
}
```

---

## 8. iptables Chain Architecture

### 8.1 Chain Flow

```
PREROUTING (mangle)
    └→ PRIVSTACK_PRE
        ├→ DIVERT (established connections: mark + accept)
        ├→ PRIVSTACK_BYPASS (reserved IPs: RETURN)
        └→ TPROXY --on-port 12345 --tproxy-mark 0x1 (TCP+UDP)

OUTPUT (mangle)
    └→ PRIVSTACK_OUT
        ├→ GID match 23333: RETURN (loop prevention)
        ├→ PRIVSTACK_BYPASS (reserved IPs: RETURN)
        ├→ PRIVSTACK_APP (per-app UID filter)
        └→ MARK --set-mark 0x1 (TCP+UDP)
```

### 8.2 Policy Routing

```bash
ip rule add fwmark 0x1 table 100 pref 100
ip route add local 0.0.0.0/0 dev lo table 100
```

Marked packets in OUTPUT → re-routed to loopback → hit PREROUTING → TPROXY catches them.

### 8.3 Packet Flow (Complete)

```
App sends TCP SYN to 93.184.216.34:443
  │
  ▼ OUTPUT chain (mangle)
  PRIVSTACK_OUT: not GID 23333, not LAN → MARK 0x1
  │
  ▼ Routing re-lookup (mark changed)
  ip rule: fwmark 0x1 → table 100 → local default dev lo
  │
  ▼ PREROUTING chain (mangle) — packet on loopback
  PRIVSTACK_PRE: TPROXY → 127.0.0.1:12345, mark 0x1
  │
  ▼ xray receives (original dst = 93.184.216.34:443 preserved!)
  → sniffs TLS SNI → routing decision → VLESS-Reality outbound
  → sends to proxy server (GID 23333, bypasses iptables)
  │
  ▼ Normal routing → physical interface → internet
```

### 8.4 Reserved IP Bypass

```
0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8,
169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.0.2.0/24,
192.168.0.0/16, 198.18.0.0/15, 198.51.100.0/24, 203.0.113.0/24,
224.0.0.0/3
```

### 8.5 Per-App Filtering

UID resolution from `/data/system/packages.list`:

```bash
# Blacklist mode: listed apps BYPASS proxy
iptables -t mangle -A PRIVSTACK_APP -m owner --uid-owner 10150 -j RETURN
# → everything else: falls through to MARK (proxied)

# Whitelist mode: ONLY listed apps are proxied
iptables -t mangle -A PRIVSTACK_APP -m owner --uid-owner 10100 -j MARK --set-mark 0x1
# → terminal: -j RETURN (everything else direct)
```

### 8.6 DNS Hijack

```bash
# Disable Android Private DNS (DoT bypass prevention)
settings put global private_dns_mode off

# In TPROXY mode: DNS UDP goes through same TPROXY chain naturally
# In REDIRECT mode: explicit nat DNAT
iptables -t nat -A OUTPUT -p udp --dport 53 \
  -m owner ! --gid-owner 23333 -j DNAT --to 127.0.0.1:1053
```

### 8.7 DIVERT Optimization

For already-established connections (performance):

```bash
iptables -t mangle -N DIVERT
iptables -t mangle -A DIVERT -j MARK --set-mark 0x1
iptables -t mangle -A DIVERT -j ACCEPT
iptables -t mangle -I PREROUTING -p tcp -m socket --transparent -j DIVERT
```

### 8.8 REDIRECT Fallback

When kernel lacks `CONFIG_NETFILTER_XT_TARGET_TPROXY`:

```bash
# nat table instead of mangle, TCP only (UDP unsupported)
iptables -t nat -A PRIVSTACK_NAT_OUT -p tcp -j REDIRECT --to-ports 12345
```

Xray uses `redirect` sockopt instead of `tproxy`. Lost: UDP proxying.

---

## 9. APK Controller: PrivStack Panel

### 9.1 Design Principles

- **Package name:** `com.privstack.panel` (no VPN/proxy keywords)
- **No `INTERNET` permission** — all networking through root daemon
- **No `BIND_VPN_SERVICE`** — not a VPN app
- **No services** — pure UI controller
- **Only permission:** `CAMERA` (optional, for QR code scanning)

### 9.2 Manifest

```xml
<manifest package="com.privstack.panel">
    <!-- NO android.permission.INTERNET -->
    <!-- NO android.permission.BIND_VPN_SERVICE -->
    <uses-permission android:name="android.permission.CAMERA" />
    <queries>
        <intent><action android:name="android.intent.action.MAIN" /></intent>
    </queries>
    <application android:allowBackup="false">
        <activity android:name=".MainActivity"
            android:exported="true" android:launchMode="singleTask">
            <intent-filter>
                <action android:name="android.intent.action.MAIN" />
                <category android:name="android.intent.category.LAUNCHER" />
            </intent-filter>
        </activity>
        <!-- No services. No VPN. No foreground service. -->
    </application>
</manifest>
```

### 9.3 IPC Transport

```
APK (unprivileged)
  → Runtime.exec("su -c /data/adb/privstack/bin/privctl <cmd> [json]")
    → privctl connects to /data/adb/privstack/run/daemon.sock
      → privd processes command
        → JSON response to stdout
```

Each command is stateless and short-lived. No persistent root shell.

### 9.4 Architecture (Kotlin)

```
com.privstack.panel/
├── ipc/
│   ├── PrivctlExecutor.kt        # su -c privctl execution
│   ├── PrivctlResult.kt          # sealed: Success | Error | RootDenied | Timeout
│   ├── DaemonClient.kt           # typed suspend methods
│   └── PollingStatusSource.kt    # coroutine-based poller (2s connected, 10s idle)
├── model/
│   ├── Node.kt                   # server node (mirrors link_parser.py)
│   ├── ProfileConfig.kt          # full profile: node + routing + dns + per-app
│   ├── DaemonStatus.kt           # state, uptime, server, egress IP, traffic
│   ├── HealthReport.kt           # health check results
│   ├── AuditReport.kt            # audit findings with severity
│   ├── AppInfo.kt                # installed app metadata
│   └── LogEntry.kt               # timestamp + level + message
├── repository/
│   ├── ProfileRepository.kt      # CRUD via DaemonClient
│   ├── StatusRepository.kt       # wraps PollingStatusSource
│   ├── AuditRepository.kt        # audit execution + caching
│   └── LogRepository.kt          # log fetch + streaming
├── import/
│   ├── LinkParser.kt             # VLESS, VMess, Trojan, SS, SOCKS URIs
│   ├── AmneziaImporter.kt        # vpn:// format (qCompress + base64url)
│   ├── SubscriptionHandler.kt    # subscription import via daemon
│   └── QrScanHandler.kt          # CameraX + ML Kit barcode
├── advisor/
│   ├── AppClassifier.kt          # classify by risk: banking, telecom, marketplace
│   ├── PlacementAdvisor.kt       # recommend Work Profile placement
│   └── ClassificationDb.kt       # bundled JSON package→category mapping
├── ui/
│   ├── dashboard/                # Connection card, traffic, latency, health
│   ├── config/                   # Profile list, editor, import, per-app picker
│   ├── audit/                    # Security audit + App Placement Advisor
│   ├── logs/                     # Log viewer + streaming
│   └── settings/                 # Theme, daemon config, about
└── di/
    └── AppModule.kt              # Hilt: PrivctlExecutor, DaemonClient, repos
```

### 9.5 UI Navigation (4 tabs)

| Tab | Content |
|-----|---------|
| **Dashboard** | Connection state, connect/disconnect, server info, traffic rates, egress IP, latency, DNS status |
| **Config** | Profile list, profile editor (node, routing, DNS, per-app), import (URI/QR/subscription) |
| **Audit** | Security audit (14 checks), App Placement Advisor, risk report |
| **Settings** | Theme, daemon path, versions, logs sub-route |

### 9.6 Timeout Strategy

| Command | Timeout | Rationale |
|---------|---------|-----------|
| status, version, config-get | 5s | Fast local reads |
| start, stop, reload | 10s | Process lifecycle |
| health | 15s | May probe connectivity |
| audit | 30s | Scans packages, profiles, network |
| config-import (subscription) | 30s | Network fetch through daemon |

---

## 10. IPC Protocol

### 10.1 Wire Format

JSON-RPC 2.0 over Unix domain socket, newline-delimited.

**Request:**
```json
{"jsonrpc":"2.0","id":"uuid","method":"status","params":{}}
```

**Success response:**
```json
{
  "jsonrpc": "2.0",
  "id": "uuid",
  "result": {
    "state": "connected",
    "uptime_sec": 3842,
    "server": "nl-1.example.com:443",
    "egress_ip": "185.x.x.x",
    "tx_bytes": 104857600,
    "rx_bytes": 209715200,
    "latency_ms": 42,
    "dns_ok": true,
    "active_profile": "my-server",
    "xray_pid": 12345,
    "iptables_active": true
  }
}
```

**Error response:**
```json
{
  "jsonrpc": "2.0",
  "id": "uuid",
  "error": {
    "code": -32001,
    "message": "daemon not running",
    "data": null
  }
}
```

### 10.2 Full Method Table

| Method | Params | Response | Description |
|--------|--------|----------|-------------|
| `status` | — | state, uptime, server, traffic, PIDs | Full daemon status |
| `start` | `{profile?}` | `{ok}` | Start proxy |
| `stop` | — | `{ok}` | Stop proxy + teardown iptables |
| `reload` | — | `{ok}` | Hot-reload (restart xray, keep iptables) |
| `health` | — | `{overall, checks[]}` | Health check results |
| `audit` | `{checks[]}` | `{results[], overall_risk}` | Security audit |
| `config-get` | `{profile?}` | `{config}` | Read profile config |
| `config-set` | `{profile, config}` | `{ok, warnings[]}` | Write profile config |
| `config-list` | — | `{profiles[]}` | List all profiles |
| `config-delete` | `{profile}` | `{ok}` | Delete profile |
| `config-import` | `{uri}` or `{subscription_url}` | `{profile, node}` | Import URI/subscription |
| `config-export` | `{profile, format}` | `{data}` | Export profile |
| `logs` | `{lines?, level?}` | `{entries[]}` | Fetch recent logs |
| `logs-stream` | `{level?}` | streaming NDJSON | Live log stream |
| `resolve-uid` | `{packages[]}` | `{mappings[]}` | Package → UID |
| `app-list` | — | `{apps[]}` | Installed apps with UIDs |
| `routing-set` | `{rules}` | `{ok}` | Update routing rules |
| `version` | — | `{daemon, xray, privctl, module}` | Version info |

### 10.3 Error Codes

| Code | Meaning |
|------|---------|
| -32700 | Parse error (malformed JSON) |
| -32601 | Method not found |
| -32001 | Daemon not running |
| -32002 | Root access denied |
| -32003 | Config validation failed |
| -32004 | Profile not found |
| -32005 | Already running / already stopped |
| -32006 | Xray core binary missing |
| -32010 | Timeout (daemon unresponsive) |

---

## 11. Isolation Layer

### 11.1 Strategy: Work Profile Separation

Instead of hooking app processes (like Hide-My-Applist), use Android's built-in Work Profile for physical namespace separation.

```
Personal Profile                    Work Profile (isolated)
├── Browser                         ├── Sberbank
├── Telegram                        ├── T-Bank
├── YouTube                         ├── VTB
├── privstack APK                   ├── Alfa-Bank
├── Terminal emulator               ├── Samokat
└── (proxy tools)                   ├── MegaMarket
                                    ├── Yandex.Go
                                    └── (aggressive apps)
```

**Why this works:** Apps in Work Profile cannot see packages installed in Personal Profile. `PackageManager.getInstalledPackages()` returns only Work Profile packages. No VPN apps visible, no proxy tools visible.

### 11.2 App Risk Classification

| Category | Risk | Profile | Examples |
|----------|------|---------|----------|
| Banking | CRITICAL | Work only | Sberbank, T-Bank, VTB, Alfa-Bank |
| Marketplace | HIGH | Work only | Samokat, MegaMarket, Ozon |
| Telecom | HIGH | Work only | MTS, Megafon, Beeline |
| Government | HIGH | Work only | Gosuslugi, Nalog |
| Ecosystem | MEDIUM | Work preferred | Yandex, VK, Mail.ru |
| Social | LOW | Either | Instagram, TikTok |
| Messaging | LOW | Either | WhatsApp, Telegram |
| Streaming | LOW | Personal preferred | Netflix, YouTube |
| Tools | NONE | Personal | Browser, Terminal, privstack |

### 11.3 Cross-Boundary Audit

The APK's audit module checks:
- Shared contacts between profiles
- Shared files / external storage access
- Clipboard sharing enabled?
- Cross-profile intents leaking data
- Account sync across profiles

### 11.4 Alternative: Hide-My-Applist (LSPosed)

If Work Profile is not feasible, HMA provides software-level hiding:

- Hooks `shouldFilterApplication` in system_server (single hook point)
- Blocks all PackageManager queries: `getInstalledPackages()`, `queryIntentActivities()`, etc.
- Template system: define "hide these packages from these apps"
- **Limitation:** Requires LSPosed/Xposed (itself detectable)
- **Limitation:** Does not hide `/proc/<pid>/maps` of running processes
- **Limitation:** Does not hide external storage paths without vold isolation

**Recommendation:** Work Profile is preferred (no hooks, no Xposed). HMA as fallback.

---

## 12. Audit System

### 12.1 Audit Checks (Run via `privctl audit`)

| Check ID | Category | What It Verifies | Severity |
|----------|----------|------------------|----------|
| `vpn_api_surface` | Network | No TRANSPORT_VPN visible to apps | CRITICAL |
| `vpn_service_binding` | Network | No VpnService bound in system | CRITICAL |
| `tun_interface_visible` | Network | No tun/ppp/wg interfaces | HIGH |
| `connectivity_vpn_flag` | Network | NOT_VPN capability present | CRITICAL |
| `proc_net_leak` | Network | Proxy port not visible in /proc/net/tcp | HIGH |
| `xray_api_protected` | Network | API port restricted to UID 0 | MEDIUM |
| `iptables_loop_safe` | Network | GID exemption in OUTPUT chain | HIGH |
| `dns_leak` | Network | DNS goes through proxy, not ISP | MEDIUM |
| `package_visibility` | Packages | VPN/proxy packages not visible to risk apps | HIGH |
| `package_name_safe` | Packages | APK package name has no VPN keywords | LOW |
| `profile_boundary` | Isolation | Work Profile data doesn't leak | HIGH |
| `profile_clipboard` | Isolation | Cross-profile clipboard disabled | MEDIUM |
| `selinux_status` | System | SELinux enforcing | HIGH |
| `module_integrity` | System | Binary checksums match | MEDIUM |

### 12.2 Output Format

```json
{
  "timestamp": "2026-04-11T15:30:00Z",
  "overall_risk": "LOW",
  "results": [
    {
      "check": "vpn_api_surface",
      "category": "Network",
      "severity": "CRITICAL",
      "pass": true,
      "title": "No VPN visible to apps",
      "detail": "NetworkCapabilities shows TRANSPORT_WIFI, NOT_VPN present",
      "remediation": null
    },
    {
      "check": "xray_api_protected",
      "category": "Network",
      "severity": "MEDIUM",
      "pass": false,
      "title": "Xray API port accessible",
      "detail": "Port 8080 on localhost reachable from UID 10150",
      "remediation": "Add iptables rule to restrict API port to UID 0"
    }
  ]
}
```

---

## 13. Amnezia VPN + VLESS Hiding Plan

### 13.1 Protocol Support Matrix

| Protocol | Can Use tproxy? | Hiding Level | Notes |
|----------|:--------------:|:------------:|-------|
| **VLESS-Reality** (xray) | **YES** | Full | Native `dokodemo-door` tproxy support |
| **VLESS** (xray) | **YES** | Full | Same as above |
| **VMess** (xray) | **YES** | Full | Same as above |
| **Trojan** (xray) | **YES** | Full | Same as above |
| **Shadowsocks** (xray) | **YES** | Full | SSXray via xray-core |
| **AmneziaWG** | **NO** | Partial | Tunnel protocol, requires TUN interface |
| **WireGuard** | **NO** | Partial | Same as AWG |
| **OpenVPN** | **NO** | None | Not a proxy protocol |
| **OpenVPN+Cloak** | **NO** | None | Same as OpenVPN |

### 13.2 Recommended Configuration

**For maximum invisibility: VLESS-Reality through privstack**

1. Export VLESS config from Amnezia server (or any xray server)
2. Import via `privctl config-import "vless://..."` or APK import
3. privstack renders xray config with tproxy inbound + VLESS-Reality outbound
4. No VPN indicators on device whatsoever

### 13.3 Amnezia Config Import

Amnezia uses `vpn://` URIs (qCompress + base64url JSON). The APK's `AmneziaImporter` extracts the xray outbound config:

```
vpn://base64url(qCompress(json))
  → decompress → parse JSON
  → extract container "xray" → extract outbound config
  → convert to canonical ProfileConfig
```

### 13.4 v2rayNG Config Import

Standard URI formats directly supported:

```
vless://uuid@host:port?security=reality&sni=...&fp=chrome&pbk=...&sid=...#name
```

Parsed into `Node` → rendered into xray outbound by privd.

### 13.5 What Remains Detectable

Even with privstack + Work Profile:

| Still Visible | Why | Mitigation |
|---------------|-----|------------|
| Foreign IP address | Server-side check | Use residential exit IP |
| GeoIP + cell tower mismatch | Physical vs IP location | Use same-country exit |
| TLS fingerprint of outer connection | JA3/JA4 analysis | VLESS-Reality mimics Chrome |
| Parallel request timing | Split-routing detection | Use global proxy mode |
| `/proc/net/tcp` proxy port | Localhost listener (Android 10+ blocks) | Non-standard port |

---

## 14. Boot Sequence

```
STAGE                      ACTION
─────────────────────────  ──────────────────────────────────
Kernel init                (nothing)

post-fs-data [blocking]    post-fs-data.sh:
                             Create dirs, set permissions
                             Enable ip_forward
                             Verify binaries

Zygote starts              (nothing)

late_start service         service.sh:
                             Check manual flag
                             Wait for sys.boot_completed
                             Sleep 5s (network settle)
                             Launch privd daemon

privd init                 Open Unix socket
                           Read config.json
                           If autostart=true → start()

privd start()              Render xray config
                           Spawn xray (gid=23333)
                           Wait for listen (30s)
                           Backup iptables
                           Apply iptables chains
                           Apply DNS hijack
                           Health check
                           → RUNNING

Steady state               Health loop every 30s
                           IPC listener on daemon.sock
                           Signal handler (SIGTERM→stop)

Network change             inotifyd on /data/misc/net/
                           Update bypass rules for local IPs
```

---

## 15. Canonical Config Schema

```json
{
  "proxy": {
    "mode": "tproxy",           // "tproxy" | "redirect"
    "port": 12345,              // proxy listen port
    "dns_port": 1053,           // DNS listen port
    "fwmark": 1,                // iptables fwmark value
    "route_table": 100,         // policy routing table
    "core_gid": 23333           // xray process GID (loop prevention)
  },
  "transport": {
    "core": "xray",             // currently only xray
    "log_level": "error"        // none | error | warning | info | debug
  },
  "node": {
    "active": "my-server",
    "list": [
      {
        "name": "my-server",
        "protocol": "vless",
        "address": "server.example.com",
        "port": 443,
        "uuid": "...",
        "flow": "xtls-rprx-vision",
        "security": "reality",
        "sni": "www.example.com",
        "fingerprint": "chrome",
        "public_key": "...",
        "short_id": "..."
      }
    ]
  },
  "routing": {
    "mode": "global",           // "global" | "rule" | "direct"
    "bypass_lan": true,
    "bypass_cn": false,         // bypass Chinese IPs (ipset)
    "block_quic": false,
    "custom_bypass_ips": [],
    "custom_direct_domains": [],
    "custom_proxy_domains": [],
    "custom_block_domains": []
  },
  "apps": {
    "mode": "all",              // "all" | "blacklist" | "whitelist"
    "list": []                  // package names
  },
  "dns": {
    "hijack": true,
    "disable_private_dns": false,
    "proxy_dns": "https://1.1.1.1/dns-query",
    "direct_dns": "223.5.5.5"
  },
  "ipv6": {
    "mode": "mirror"            // "mirror" (full ip6tables mirror) | "disable"
  },
  "health": {
    "enabled": true,
    "interval_seconds": 30,
    "failure_threshold": 3,
    "connectivity_check": true
  },
  "rescue": {
    "enabled": true,
    "max_attempts": 3,
    "rollback_on_failure": true
  },
  "autostart": true
}
```

---

## 16. File Layout (Complete)

```
/data/adb/modules/privstack/          # Magisk module root
├── module.prop                        # Module metadata
├── post-fs-data.sh                    # Early init (dirs, perms, ip_forward)
├── service.sh                         # Boot launcher (starts privd)
├── customize.sh                       # Install script
├── sepolicy.rule                      # SELinux policy
├── uninstall.sh                       # Removal cleanup
└── system/                            # Empty (no system overlay)

/data/adb/privstack/                   # Persistent data
├── bin/
│   ├── privd                          # Root daemon
│   ├── privctl                        # CLI client
│   └── xray                           # Transport core
├── config/
│   ├── config.json                    # Canonical config
│   ├── manual                         # Flag: disable autostart
│   └── rendered/
│       └── xray.json                  # Generated xray config
├── profiles/
│   └── <name>/
│       └── config.json                # Per-profile config
├── scripts/
│   ├── iptables.sh                    # Chain setup/teardown
│   ├── routing.sh                     # ip rule/route management
│   └── dns.sh                         # DNS hijack
├── run/
│   ├── daemon.sock                    # Unix domain socket
│   ├── daemon.pid                     # privd PID
│   ├── xray.pid                       # xray PID
│   ├── state.json                     # Current daemon state
│   ├── health.json                    # Latest health check
│   └── runtime_iptables.conf         # Params snapshot for teardown
├── logs/
│   ├── privd.log                      # Daemon log
│   └── xray_error.log                # xray stderr
├── backup/
│   ├── iptables_pre.rules            # Pre-start iptables dump
│   └── dns_pre.conf                  # Original Private DNS setting
└── templates/
    └── xray_tproxy.json.tmpl         # xray config template
```

---

## References

- [box4magisk](https://github.com/CHIZI-0618/box4magisk) — Reference tproxy Magisk module
- [NetProxy-Magisk](https://github.com/Fanju6/NetProxy-Magisk) — xray-specific tproxy module with API hot-swap
- [Hide-My-Applist](https://github.com/Dr-TSNG/Hide-My-Applist) — Package visibility hiding via LSPosed
- [v2rayNG](https://github.com/2dust/v2rayng) — VLESS client architecture reference
- [Amnezia VPN](https://github.com/amnezia-vpn/amnezia-client) — Multi-protocol VPN client
- [RKNHardering](https://github.com/xtclovver/RKNHardering) — VPN detection methodology (threat model)
- [RKS Global VPN Detection Research](https://rks.global/ru/research/vpn-detection/) — 30-app detection study
- [XTLS Transparent Proxy Guide](https://xtls.github.io/en/document/level-2/transparent_proxy/)
- [Linux Kernel TPROXY Documentation](https://docs.kernel.org/networking/tproxy.html)
