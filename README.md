# PrivStack

**Transparent proxy for rooted Android — invisible to VPN detection.**

No VPN. No TUN. No icon. No `TRANSPORT_VPN`. Banking apps see nothing.

## How It Works

```
App traffic → iptables TPROXY → sing-box → proxy server
```

- Uses kernel-level `tproxy + iptables` instead of Android VPN
- No `VpnService`, no TUN interface, no status bar icon
- **Whitelist mode**: only selected apps go through proxy, everything else is direct
- Full IPv4 + IPv6 support with mirrored rules
- Tested against [RKNHardering](https://github.com/xtclovver/RKNHardering) → **NOT_DETECTED**

## Components

| Component | Description |
|-----------|-------------|
| **Magisk Module** | Shell scripts + Go daemon. Manages sing-box, iptables rules, DNS, health monitoring |
| **Android APK** | Kotlin + Jetpack Compose controller. Zero network permissions. Talks to daemon via `su` |

## Quick Start

1. Download from [Releases](../../releases/latest):
   - `privstack-vX.X.X-module.zip` — Magisk module
   - `privstack-vX.X.X-panel.apk` — Controller app

2. Flash module via **Magisk Manager** / **KSU Manager** / **APatch**

3. Reboot

4. Install and open the APK

5. Add a server: paste a `vless://` or `trojan://` link

6. Go to **Apps** tab → select which apps to proxy

7. Tap **Connect**

## Supported Protocols

| Protocol | Status |
|----------|--------|
| VLESS + Reality | Primary (recommended) |
| VLESS + TLS | Supported |
| Trojan | Supported |
| VMess | Supported |
| Shadowsocks 2022 | Supported |
| Hysteria2 | Planned (sing-box supports it, URI parser coming) |
| TUIC v5 | Planned (sing-box supports it, URI parser coming) |
| AmneziaWG | Planned (via wireproxy-awg) |

## Import Formats

- `vless://`, `vmess://`, `trojan://`, `ss://` URIs
- Amnezia `vpn://` format
- Subscription URLs (base64-encoded URI lists)
- QR codes
- Clash YAML (Meta)
- v2rayNG JSON backup

## Detection Resistance

Tested against all 6 modules of [RKNHardering](https://github.com/xtclovver/RKNHardering):

| Check | Result |
|-------|--------|
| `TRANSPORT_VPN` flag | Not set (no VpnService) |
| TUN interface (`tun0`) | Not created |
| VPN status bar icon | Not shown |
| `NOT_VPN` capability | Present |
| Package scan (23 VPN apps) | Not found (APK has no VPN indicators) |
| SOCKS5/HTTP proxy scan | Not detected (TPROXY ≠ SOCKS5) |
| Xray gRPC API scan | Blocked (iptables root-only) |
| `/proc/net/tcp` scan | Invisible (SELinux, Android 10+) |
| **VerdictEngine result** | **NOT_DETECTED** |

## APK Features

- **Dashboard**: connection state, traffic graph, egress IP, latency
- **Nodes**: server list with groups, latency test, import (paste/QR/subscription)
- **Apps**: whitelist picker with templates (Browsers, Social, Streaming)
- **Audit**: 14 security checks + Work Profile placement advisor
- Material 3 with Dynamic Color (Android 12+)
- Russian + English

## Building

### Prerequisites

- Go 1.22+ (for daemon)
- JDK 17 + Android SDK (for APK)
- Linux/macOS (for cross-compilation)

### Quick Build

```bash
make all
```

### Individual Components

```bash
make daemon    # Build privd + privctl (arm64)
make singbox   # Download sing-box binary
make module    # Assemble Magisk ZIP
make apk       # Build Android APK
```

### CI/CD

Push a tag to trigger automatic release:

```bash
git tag v1.0.0
git push origin v1.0.0
```

GitHub Actions will build everything and create a release with downloadable artifacts.

## Architecture

```
┌─────────────────────────────────────────────┐
│              Android Device (rooted)         │
│                                              │
│  ┌──────────┐        ┌────────────────────┐  │
│  │ APK      │ su -c  │ Magisk Module      │  │
│  │ (no NET) │──────→ │ ┌────────────────┐ │  │
│  │          │ privctl│ │ privd (daemon)  │ │  │
│  └──────────┘        │ │ state machine   │ │  │
│                      │ │ health monitor  │ │  │
│                      │ └───────┬────────┘ │  │
│                      │ ┌───────▼────────┐ │  │
│                      │ │ sing-box       │ │  │
│                      │ │ tproxy inbound │ │  │
│                      │ └───────┬────────┘ │  │
│                      │ ┌───────▼────────┐ │  │
│                      │ │ iptables       │ │  │
│                      │ │ mangle TPROXY  │ │  │
│                      │ │ IPv4 + IPv6    │ │  │
│                      │ └────────────────┘ │  │
│                      └────────────────────┘  │
└─────────────────────────────────────────────┘
```

## License

MIT

## Credits

- [sing-box](https://github.com/SagerNet/sing-box) — transport core
- [box_for_magisk](https://github.com/taamarin/box_for_magisk) — reference implementation
- [RKNHardering](https://github.com/xtclovver/RKNHardering) — threat model / test suite
