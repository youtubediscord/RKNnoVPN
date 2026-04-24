package com.privstack.panel.advisor

import javax.inject.Inject
import javax.inject.Singleton

/**
 * Recommended Android profile placement for an app.
 */
enum class ProfilePlacement(val displayName: String) {
    /** App should stay in the Personal profile (can be proxied freely). */
    PERSONAL("Personal"),
    /** App should be moved to the Work Profile (isolated from proxy tools). */
    WORK("Work Profile"),
}

/**
 * Urgency level for a placement recommendation.
 */
enum class PlacementUrgency {
    /** Current placement is correct -- no action needed. */
    CORRECT,
    /** User should consider moving the app but it is not critical. */
    CONSIDER,
    /** App is in the wrong profile and may leak or malfunction. */
    MISPLACED,
}

/**
 * A single placement recommendation produced by [PlacementAdvisor].
 */
data class PlacementRecommendation(
    val app: ClassifiedApp,
    /** Where the advisor thinks the app should live. */
    val recommended: ProfilePlacement,
    /** Where the app currently is (null if unknown). */
    val current: ProfilePlacement?,
    val urgency: PlacementUrgency,
    val reason: String,
)

/**
 * Recommends which Android profile (Personal vs Work) each installed app
 * should live in for optimal privacy and functionality when root TPROXY
 * routing is active.
 *
 * **General strategy:**
 * - Banking, Government, and Telecom apps should run in an isolated Work
 *   Profile where they are less exposed to proxy tooling and routing artifacts.
 * - Browsers, Social/Messaging, and Streaming apps benefit from proxy
 *   coverage and belong in the Personal profile.
 * - VPN/Proxy apps must remain in Personal (they are the proxy tooling).
 * - System apps are left untouched.
 * - Everything else defaults to Personal with a "consider" note.
 */
@Singleton
class PlacementAdvisor @Inject constructor(
    private val classifier: AppClassifier,
) {

    /**
     * Produce placement recommendations for a list of installed apps.
     *
     * @param apps              List of (packageName, label) pairs.
     * @param currentPlacements Optional map of packageName -> current [ProfilePlacement].
     *                          When provided, the advisor can flag misplaced apps.
     * @return Sorted list of recommendations (misplaced first, then consider, then correct).
     */
    fun advise(
        apps: List<Pair<String, String>>,
        currentPlacements: Map<String, ProfilePlacement> = emptyMap(),
    ): List<PlacementRecommendation> {
        val classified = classifier.classifyAll(apps)
        return classified.map { app ->
            recommend(app, currentPlacements[app.packageName])
        }.sortedWith(
            compareBy(
                { it.urgency.ordinal },                // CORRECT=0, CONSIDER=1, MISPLACED=2 -- reversed below
                { it.app.category.ordinal },
                { it.app.label.lowercase() },
            )
        ).sortedByDescending { it.urgency.ordinal }   // MISPLACED shown first
    }

    /**
     * Count how many apps need attention (CONSIDER or MISPLACED).
     */
    fun countNeedingAttention(recommendations: List<PlacementRecommendation>): Int =
        recommendations.count { it.urgency != PlacementUrgency.CORRECT }

    /**
     * Group recommendations by category for display.
     */
    fun groupByCategory(
        recommendations: List<PlacementRecommendation>,
    ): Map<AppCategory, List<PlacementRecommendation>> =
        recommendations.groupBy { it.app.category }

    // ------------------------------------------------------------------ //
    //  Internal
    // ------------------------------------------------------------------ //

    private fun recommend(
        app: ClassifiedApp,
        currentPlacement: ProfilePlacement?,
    ): PlacementRecommendation {
        val (recommended, reason) = when (app.category) {
            AppCategory.BANKING -> ProfilePlacement.WORK to
                "Banking apps may refuse to run when they detect VPN or proxy artifacts. " +
                "Isolate in Work Profile to reduce exposure."

            AppCategory.GOVERNMENT -> ProfilePlacement.WORK to
                "Government apps often perform environment checks. " +
                "Work Profile isolation prevents proxy detection."

            AppCategory.TELECOM -> ProfilePlacement.WORK to
                "Telecom apps may report network anomalies. " +
                "Keeping them in the Work Profile avoids interference."

            AppCategory.BROWSER -> ProfilePlacement.PERSONAL to
                "Browsers benefit from proxy coverage for bypassing restrictions."

            AppCategory.SOCIAL_MESSAGING -> ProfilePlacement.PERSONAL to
                "Messaging apps should be proxied for censorship resistance."

            AppCategory.STREAMING -> ProfilePlacement.PERSONAL to
                "Streaming apps may need the proxy to access geo-restricted content."

            AppCategory.VPN_PROXY -> ProfilePlacement.PERSONAL to
                "Proxy/VPN tools should stay with the PrivStack control app and outside the proxied app set."

            AppCategory.SYSTEM -> ProfilePlacement.PERSONAL to
                "System apps are managed by the OS and should not be moved."

            AppCategory.OTHER -> ProfilePlacement.PERSONAL to
                "No specific risk detected. Personal profile is the default."
        }

        val urgency = when {
            currentPlacement == null -> {
                // We don't know the current placement; base urgency on category alone
                when (app.category) {
                    AppCategory.BANKING, AppCategory.GOVERNMENT -> PlacementUrgency.CONSIDER
                    AppCategory.TELECOM -> PlacementUrgency.CONSIDER
                    else -> PlacementUrgency.CORRECT
                }
            }
            currentPlacement == recommended -> PlacementUrgency.CORRECT
            // App is in the wrong profile
            recommended == ProfilePlacement.WORK && currentPlacement == ProfilePlacement.PERSONAL ->
                PlacementUrgency.MISPLACED
            recommended == ProfilePlacement.PERSONAL && currentPlacement == ProfilePlacement.WORK ->
                PlacementUrgency.CONSIDER
            else -> PlacementUrgency.CORRECT
        }

        return PlacementRecommendation(
            app = app,
            recommended = recommended,
            current = currentPlacement,
            urgency = urgency,
            reason = reason,
        )
    }
}
