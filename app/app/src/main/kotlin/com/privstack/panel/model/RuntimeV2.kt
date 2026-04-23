package com.privstack.panel.model

import kotlinx.serialization.Serializable

@Serializable
enum class BackendKind {
    ROOT_TPROXY,
}

@Serializable
enum class FallbackPolicy {
    OFFER_RESET,
    STAY_ON_SELECTED,
    AUTO_RESET_ROOTED,
}

@Serializable
data class RuntimeConfig(
    val backendKind: BackendKind = BackendKind.ROOT_TPROXY,
    val fallbackPolicy: FallbackPolicy = FallbackPolicy.OFFER_RESET,
)

@Serializable
enum class BackendPhase {
    STOPPED,
    APPLYING,
    HEALTHY,
    DEGRADED,
}

@Serializable
data class BackendCapability(
    val kind: BackendKind,
    val supported: Boolean,
    val reason: String = "",
)

@Serializable
data class DesiredStateV2(
    val backendKind: BackendKind = BackendKind.ROOT_TPROXY,
    val activeProfileId: String? = null,
    val routingMode: String = "",
    val fallbackPolicy: FallbackPolicy = FallbackPolicy.OFFER_RESET,
)

@Serializable
data class AppliedStateV2(
    val backendKind: BackendKind = BackendKind.ROOT_TPROXY,
    val phase: BackendPhase = BackendPhase.STOPPED,
    val activeProfileId: String? = null,
    val startedAt: String? = null,
    val generation: Long = 0L,
)

@Serializable
data class BackendHealthSnapshot(
    val coreReady: Boolean = false,
    val dnsReady: Boolean = false,
    val routingReady: Boolean = false,
    val egressReady: Boolean = false,
    val lastError: String = "",
    val checkedAt: String? = null,
) {
    val healthy: Boolean
        get() = coreReady && dnsReady && routingReady && egressReady
}

@Serializable
data class BackendStatusV2(
    val desiredState: DesiredStateV2 = DesiredStateV2(),
    val appliedState: AppliedStateV2 = AppliedStateV2(),
    val health: BackendHealthSnapshot = BackendHealthSnapshot(),
    val capabilities: List<BackendCapability> = emptyList(),
)

@Serializable
data class ResetStep(
    val name: String,
    val status: String,
    val detail: String = "",
)

@Serializable
data class ResetReport(
    val backendKind: BackendKind = BackendKind.ROOT_TPROXY,
    val generation: Long = 0L,
    val status: String = "ok",
    val steps: List<ResetStep> = emptyList(),
    val errors: List<String> = emptyList(),
)

@Serializable
data class NodeProbeResultV2(
    val id: String = "",
    val name: String = "",
    val protocol: String = "",
    val server: String = "",
    val port: Int = 0,
    val tcpDirect: Long? = null,
    val tunnelDelay: Long? = null,
    val dnsBootstrap: Boolean = false,
    val errorClass: String = "",
)
