# RKNnoVPN Module Ownership

This module is split by root-runtime responsibility, not by historical script
entrypoint.

## Entry Points

- `customize.sh` installs files, preserves user config, records the release
  catalog, and disables autostart until the app starts the runtime.
- `post-fs-data.sh` is early boot only: create the data skeleton, apply basic
  permissions, and set kernel toggles. It must not clean runtime markers.
- `service.sh` is late boot only: wait for boot completion, ask
  `rknnovpn_env.sh` whether boot cleanup markers exist, run canonical boot
  cleanup, and launch `daemon`. It must not implement its own stale
  process/socket/PID cleanup.
- `scripts/rescue_reset.sh` is the canonical root cleanup API.
- `uninstall.sh` delegates runtime cleanup to `scripts/rescue_reset.sh` and
  only handles uninstall-specific preservation/restoration.

## Shared Runtime Library

Module scripts should use the shared files under `scripts/lib/`:

- `rknnovpn_env.sh` owns canonical paths, marker helpers, and shared
  permission/layout helpers.
- `rknnovpn_install.sh` owns install/release-catalog helper behavior shared by
  Magisk/KSU/APatch installation and staged module updates.
- `rknnovpn_installer_flow.sh` owns the Magisk/KSU/APatch install flow:
  preflight, config preservation, binary/script copy, release catalog, and
  permissions. `customize.sh` must stay a thin entrypoint.
- `rknnovpn_netstack.sh` owns generic RKNnoVPN netfilter/policy-routing
  cleanup helpers used by rescue/uninstall paths.
- `rknnovpn_iptables_rules.sh` owns TPROXY rule rendering and listener
  protection verification. `scripts/iptables.sh` must stay orchestration:
  validate env, snapshot, apply, teardown, and status dispatch.

Both Magisk/KSU/APatch installation (`customize.sh`) and the daemon hot-update
installer must treat all `scripts/lib/*.sh` files as required module files.

The library owns these path names:

- `RKNNOVPN_DIR=/data/adb/modules/rknnovpn`
- `BIN_DIR`, `CONFIG_DIR`, `SCRIPTS_DIR`, `RUN_DIR`, `LOG_DIR`
- `RESET_LOCK`, `ACTIVE_FILE`, `MANUAL_FLAG`
- `DAEMON_PID_FILE`, `SINGBOX_PID_FILE`, `DAEMON_SOCK`

## Marker Ownership

- `run/reset.lock`
  - Created by `rescue_reset.sh` when entering reset/cleanup mode.
  - Removed by `rescue_reset.sh` after `boot-clean`, `hard-reset`, or
    `uninstall-clean`.
  - Preserved after `daemon-reset` so the daemon-owned reset window remains
    externally visible until daemon logic completes.
  - Must not be removed by `post-fs-data.sh` or `service.sh`.

- `config/manual`
  - Created by installer and user-visible hard reset paths.
  - Prevents boot/autostart from resurrecting proxy rules.
  - `boot-clean` and `uninstall-clean` must not create it.

- `run/active`
  - Runtime liveness marker for network-change handling.
  - Cleared when entering reset/manual mode.
  - Must not be blindly removed by `post-fs-data.sh`; boot cleanup decides
    whether stale active state needs root cleanup.

## Cleanup Ownership

`scripts/rescue_reset.sh` is the only script that should implement complete
RKNnoVPN-owned cleanup of processes, DNS rules, iptables chains, policy
routing, and runtime snapshots.

Other entrypoints may call it, but should not grow a parallel netfilter cleanup
implementation. If `rescue_reset.sh` is missing, entrypoints should report that
runtime cleanup is unavailable rather than partially reimplementing process,
marker, or netfilter cleanup.

## Netstack Ownership

- `scripts/iptables.sh` owns applying/removing mangle rules, policy routing,
  and runtime snapshots in `run/env.sh`; rule text and listener verification
  belong in `scripts/lib/rknnovpn_iptables_rules.sh`.
- `scripts/dns.sh` owns classic DNS nat interception.
