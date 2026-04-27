# Changelog

## v1.8.0

- Added a runtime actor for lifecycle operations so start, stop, restart, reset, reload, network-change, rescue, and update restore no longer wait on each other through hidden long-running locks.
- Added fail-fast `RUNTIME_BUSY` / `RESET_IN_PROGRESS` JSON-RPC errors with active operation metadata, plus APK-side Russian messaging for busy operations.
- Kept `reset.lock` owned by reset/rescue cleanup paths instead of normal start or hot-swap, preserving the root-level reset guard.
- Added regression coverage for lifecycle conflicts, readable status during active operations, reset lock ownership, and generation handling.
- Extended rooted device-lab helpers with Lineage/QEMU emulator install and smoke targets.
- Synchronized release metadata for app, daemon, module, update feed, workflow stamping, and bundled scripts to `v1.8.0`.

## v1.7.3

- Fixed APK compatibility gating so a damaged `current` release catalog is shown as a repair warning instead of blocking start/restart when APK, daemon, and module versions match.
- Fixed Settings log sharing and diagnostic copy actions by using explicit one-shot UI events, Android clipboard APIs, and visible feedback.
- Rejected localhost TPROXY destinations inside the rendered sing-box route to avoid self-looping listener probes.
- Made module updates boot-safe by default: fresh configs no longer autostart, upgrades set the manual start guard until the app starts the proxy, boot cleanup only runs when stale runtime markers exist, and PrivStack workers no longer get stronger OOM priority than SystemUI.
- Hardened release catalog updates so a stale non-symlink `current` directory is moved aside and replaced with a valid release symlink in both module install and in-app update flows.

## v1.7.1

- Reworked root TPROXY runtime recovery with structured reset reports, netstack cleanup verification, and idempotent rescue behavior.
- Added `privctl doctor` diagnostics with redacted configs/logs, compatibility metadata, release integrity, netstack leftovers, node-test summary, and runtime stage reports.
- Hardened APK/module/daemon compatibility checks before mutating actions, including repair-safe reset handling.
- Split readiness, DNS, routing, egress, and node-test verdicts so TCP-only servers are not treated as usable routes.
- Kept production privacy defaults off for localhost SOCKS/HTTP/API helpers and tightened APK privacy guardrails.
- Unified runtime listener readiness for start/hot-swap across TPROXY, DNS, and optional API ports, and fixed successful start reports to finish cleanly in both status and doctor payloads.
- Extended netstack/status privacy verification to check local listener DROP rules and tightened diagnostic redaction for WireGuard/Amnezia pre-shared keys.
- Expanded self-check/doctor summary with compact compatibility, current-release, sing-box check, and runtime stage metadata for quick support triage.
- Added typed APK IPC access to `self-check` with `self.check` fallback for quick repair summaries.
- Exposed self-check through the APK status repository so Settings/Audit can use concise repair summaries without depending on raw IPC.
- Treated legacy `unknown command` audit responses like method-not-found so old daemons still fall back to local audit checks.

## v1.7.0

- Initial v1.7 runtime stabilization baseline.

## v1.6.4

- Split hard runtime readiness from soft DNS and egress diagnostics so cold-start DNS timeouts leave the runtime connected but degraded.
- Added deterministic readiness/operational diagnostics and clearer node-test reasons for runtime, proxy DNS, and HTTP helper failures.
- Added in-app display and sharing for `/data/adb/privstack/logs/privd.log` and `/data/adb/privstack/logs/sing-box.log`.

## v1.6.3

- Split hard readiness from soft DNS and egress diagnostics so restart no longer tears the runtime down on a single cold-start DNS timeout.
- Made health error reporting deterministic instead of depending on random Go map iteration order.
- Kept DNS and egress signals visible as operational diagnostics without using them as restart blockers.

## v1.6.2

- Stabilized large config/panel payload handling between APK, `privctl`, and daemon IPC.
- Added `panel.json` install/upgrade support and migration regression coverage.
- Hardened runtime sync, reset cleanup, and mixed APK/module compatibility paths.
- Require matching module and APK artifacts for in-app updates after the storage/API split.

## v1.6.1

- Finalized the Russian-first UI pass across Dashboard, Nodes, Apps, Settings, Audit, and Advisor.
- Localized daemon-generated audit findings and removed audit mapping dependence on English titles by switching to stable finding `code` values.
- Tightened runtime and control-plane user messaging for import, update, root, timeout, and daemon status flows.
- Synchronized app, daemon, module, update feed, and bundled script version metadata to `v1.6.1`.

## v1.6.0

- Russian-first localization pass across Dashboard, Nodes, Apps, Settings, Audit, and Advisor.
- Translated audit findings, advisor recommendations, runtime/import/update errors, and settings statuses.
- Switched audit finding mapping to stable `code` values so localization no longer depends on English titles.
- Synchronized release metadata for app, daemon, module, update feed, and bundled scripts to `v1.6.0`.
