# Changelog

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
