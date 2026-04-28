package com.rknnovpn.panel.ipc

// Generated from daemon/internal/ipc/contract_manifest.json.
// Run .github/scripts/check_ipc_contract_codegen.py --write after editing the IPC contract.
internal object GeneratedDaemonContract {
    const val CONTRACT_VERSION: Int = 1
    val APK_REQUIRED_METHODS: Set<String> = setOf(
        "backend.applyDesiredState",
        "backend.reset",
        "backend.restart",
        "backend.start",
        "backend.status",
        "backend.stop",
        "diagnostics.health",
        "diagnostics.report",
        "diagnostics.testNodes",
        "ipc.contract",
        "logs",
        "profile.apply",
        "profile.get",
        "profile.importNodes",
        "profile.setActiveNode",
        "self-check",
        "subscription.preview",
        "subscription.refresh",
        "update-check",
        "update-download",
        "update-install",
        "version",
    )
}
