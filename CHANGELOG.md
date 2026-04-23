# Changelog

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
