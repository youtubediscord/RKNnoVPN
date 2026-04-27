package com.rknnovpn.panel.advisor

import javax.inject.Inject
import javax.inject.Singleton

/**
 * Risk-oriented category for an installed application.
 */
enum class AppCategory(val displayName: String) {
    BROWSER("Browser"),
    SOCIAL_MESSAGING("Social / Messaging"),
    STREAMING("Streaming"),
    BANKING("Banking"),
    TELECOM("Telecom"),
    GOVERNMENT("Government"),
    VPN_PROXY("VPN / Proxy"),
    SYSTEM("System"),
    OTHER("Other"),
}

/**
 * Result of classifying a single package.
 */
data class ClassifiedApp(
    val packageName: String,
    val label: String,
    val category: AppCategory,
    /** Which matching strategy produced this classification. */
    val matchType: MatchType,
)

enum class MatchType { EXACT, PREFIX, KEYWORD, DEFAULT }

/**
 * Classifies installed applications into [AppCategory] buckets using a
 * three-tier lookup strategy:
 *
 * 1. **Exact match** -- the full package name exists in [exactMap].
 * 2. **Prefix match** -- the package name starts with a known prefix.
 * 3. **Keyword match** -- the lowercased package name contains a keyword.
 *
 * If none of the tiers match, [AppCategory.OTHER] is returned.
 */
@Singleton
class AppClassifier @Inject constructor() {

    // ------------------------------------------------------------------ //
    //  Tier 1 -- exact package-name matches
    // ------------------------------------------------------------------ //

    private val exactMap: Map<String, AppCategory> = mapOf(
        // Browsers
        "com.android.chrome" to AppCategory.BROWSER,
        "org.mozilla.firefox" to AppCategory.BROWSER,
        "org.mozilla.firefox_beta" to AppCategory.BROWSER,
        "org.mozilla.fenix" to AppCategory.BROWSER,
        "com.microsoft.emmx" to AppCategory.BROWSER,
        "com.sec.android.app.sbrowser" to AppCategory.BROWSER,
        "com.opera.browser" to AppCategory.BROWSER,
        "com.opera.mini.native" to AppCategory.BROWSER,
        "com.brave.browser" to AppCategory.BROWSER,
        "com.duckduckgo.mobile.android" to AppCategory.BROWSER,
        "com.vivaldi.browser" to AppCategory.BROWSER,
        "com.kiwibrowser.browser" to AppCategory.BROWSER,
        "com.yandex.browser" to AppCategory.BROWSER,
        "com.yandex.browser.lite" to AppCategory.BROWSER,
        "com.UCMobile.intl" to AppCategory.BROWSER,

        // Social / Messaging
        "org.telegram.messenger" to AppCategory.SOCIAL_MESSAGING,
        "org.telegram.messenger.web" to AppCategory.SOCIAL_MESSAGING,
        "org.thunderdog.chalern" to AppCategory.SOCIAL_MESSAGING,
        "com.whatsapp" to AppCategory.SOCIAL_MESSAGING,
        "com.whatsapp.w4b" to AppCategory.SOCIAL_MESSAGING,
        "org.thoughtcrime.securesms" to AppCategory.SOCIAL_MESSAGING,
        "com.discord" to AppCategory.SOCIAL_MESSAGING,
        "com.vkontakte.android" to AppCategory.SOCIAL_MESSAGING,
        "com.instagram.android" to AppCategory.SOCIAL_MESSAGING,
        "com.facebook.orca" to AppCategory.SOCIAL_MESSAGING,
        "com.facebook.katana" to AppCategory.SOCIAL_MESSAGING,
        "com.snapchat.android" to AppCategory.SOCIAL_MESSAGING,
        "com.viber.voip" to AppCategory.SOCIAL_MESSAGING,
        "jp.naver.line.android" to AppCategory.SOCIAL_MESSAGING,
        "com.tencent.mm" to AppCategory.SOCIAL_MESSAGING,
        "com.twitter.android" to AppCategory.SOCIAL_MESSAGING,
        "com.zhiliaoapp.musically" to AppCategory.SOCIAL_MESSAGING,
        "com.reddit.frontpage" to AppCategory.SOCIAL_MESSAGING,

        // Streaming
        "com.google.android.youtube" to AppCategory.STREAMING,
        "com.netflix.mediaclient" to AppCategory.STREAMING,
        "tv.twitch.android.app" to AppCategory.STREAMING,
        "com.spotify.music" to AppCategory.STREAMING,
        "com.amazon.avod.thirdpartyclient" to AppCategory.STREAMING,
        "com.disney.disneyplus" to AppCategory.STREAMING,
        "ru.more.play" to AppCategory.STREAMING,
        "com.apple.android.music" to AppCategory.STREAMING,
        "ru.kinopoisk" to AppCategory.STREAMING,

        // Banking
        "ru.sberbankmobile" to AppCategory.BANKING,
        "ru.sberbankmobile.arm" to AppCategory.BANKING,
        "ru.alfabank.mobile.android" to AppCategory.BANKING,
        "ru.tinkoff.investing" to AppCategory.BANKING,
        "ru.tinkoff.android" to AppCategory.BANKING,
        "ru.raiffeisennews" to AppCategory.BANKING,
        "com.idamob.tinkoff.android" to AppCategory.BANKING,
        "ru.rosbank.android" to AppCategory.BANKING,

        // Telecom
        "ru.mts.mymts" to AppCategory.TELECOM,
        "com.beeline.dc" to AppCategory.TELECOM,
        "ru.megafon.mlk" to AppCategory.TELECOM,
        "ru.tele2.mytele2" to AppCategory.TELECOM,
        "ru.yota.android" to AppCategory.TELECOM,

        // Government
        "ru.gosuslugi.pos" to AppCategory.GOVERNMENT,
        "ru.roskazna.gmu" to AppCategory.GOVERNMENT,
        "ru.fns.lkfl" to AppCategory.GOVERNMENT,
        "ru.nalog.ibr" to AppCategory.GOVERNMENT,
        "ru.mos.app" to AppCategory.GOVERNMENT,
        "ru.pfr.pensioner" to AppCategory.GOVERNMENT,

        // VPN / Proxy
        "com.wireguard.android" to AppCategory.VPN_PROXY,
        "org.torproject.android" to AppCategory.VPN_PROXY,
        "ch.protonvpn.android" to AppCategory.VPN_PROXY,
        "net.mullvad.mullvadvpn" to AppCategory.VPN_PROXY,
        "com.cloudflare.onedotonedotonedotone" to AppCategory.VPN_PROXY,
        "org.amnezia.vpn" to AppCategory.VPN_PROXY,
        "app.hiddify.com" to AppCategory.VPN_PROXY,
        "com.v2ray.ang" to AppCategory.VPN_PROXY,
        "io.nekohasekai.sfa" to AppCategory.VPN_PROXY,
        "io.nekohasekai.sagernet" to AppCategory.VPN_PROXY,
        "com.notcvnt.rknhardering" to AppCategory.VPN_PROXY,
        "com.yourvpndead" to AppCategory.VPN_PROXY,

        // System
        "com.android.vending" to AppCategory.SYSTEM,
        "com.google.android.gms" to AppCategory.SYSTEM,
        "com.android.settings" to AppCategory.SYSTEM,
        "com.android.systemui" to AppCategory.SYSTEM,
        "com.google.android.gsf" to AppCategory.SYSTEM,
    )

    // ------------------------------------------------------------------ //
    //  Tier 2 -- prefix matches (checked in insertion order)
    // ------------------------------------------------------------------ //

    private val prefixMap: List<Pair<String, AppCategory>> = listOf(
        "com.yandex.browser" to AppCategory.BROWSER,
        "com.opera" to AppCategory.BROWSER,

        "org.telegram" to AppCategory.SOCIAL_MESSAGING,
        "com.whatsapp" to AppCategory.SOCIAL_MESSAGING,
        "com.facebook" to AppCategory.SOCIAL_MESSAGING,
        "com.vkontakte" to AppCategory.SOCIAL_MESSAGING,

        "ru.vtb" to AppCategory.BANKING,
        "ru.sberbank" to AppCategory.BANKING,
        "ru.alfabank" to AppCategory.BANKING,
        "ru.tinkoff" to AppCategory.BANKING,
        "ru.raiffeisen" to AppCategory.BANKING,
        "ru.psbank" to AppCategory.BANKING,
        "ru.mts.bank" to AppCategory.BANKING,

        "ru.mts" to AppCategory.TELECOM,
        "com.beeline" to AppCategory.TELECOM,
        "ru.megafon" to AppCategory.TELECOM,
        "ru.tele2" to AppCategory.TELECOM,

        "ru.gosuslugi" to AppCategory.GOVERNMENT,
        "ru.fns" to AppCategory.GOVERNMENT,
        "ru.nalog" to AppCategory.GOVERNMENT,
        "ru.mos" to AppCategory.GOVERNMENT,

        "com.android" to AppCategory.SYSTEM,
        "com.google.android" to AppCategory.SYSTEM,
        "com.samsung.android" to AppCategory.SYSTEM,
    )

    // ------------------------------------------------------------------ //
    //  Tier 3 -- keyword matches (applied to lowercased package name)
    // ------------------------------------------------------------------ //

    private val keywordMap: List<Pair<String, AppCategory>> = listOf(
        "bank" to AppCategory.BANKING,
        "finance" to AppCategory.BANKING,
        "wallet" to AppCategory.BANKING,

        "vpn" to AppCategory.VPN_PROXY,
        "proxy" to AppCategory.VPN_PROXY,
        "tunnel" to AppCategory.VPN_PROXY,

        "browser" to AppCategory.BROWSER,

        "messenger" to AppCategory.SOCIAL_MESSAGING,
        "chat" to AppCategory.SOCIAL_MESSAGING,

        "stream" to AppCategory.STREAMING,
        "video" to AppCategory.STREAMING,
        "music" to AppCategory.STREAMING,

        "gosuslugi" to AppCategory.GOVERNMENT,
        "nalog" to AppCategory.GOVERNMENT,
    )

    // ------------------------------------------------------------------ //
    //  Public API
    // ------------------------------------------------------------------ //

    /**
     * Classify a single app by its package name.
     *
     * @param packageName  The Android package name (e.g. "ru.sberbankmobile").
     * @param label        Human-readable app label from PackageManager.
     */
    fun classify(packageName: String, label: String = packageName): ClassifiedApp {
        // Tier 1: exact
        exactMap[packageName]?.let { category ->
            return ClassifiedApp(packageName, label, category, MatchType.EXACT)
        }

        // Tier 2: prefix
        for ((prefix, category) in prefixMap) {
            if (packageName.startsWith(prefix)) {
                return ClassifiedApp(packageName, label, category, MatchType.PREFIX)
            }
        }

        // Tier 3: keyword
        val lower = packageName.lowercase()
        for ((keyword, category) in keywordMap) {
            if (lower.contains(keyword)) {
                return ClassifiedApp(packageName, label, category, MatchType.KEYWORD)
            }
        }

        return ClassifiedApp(packageName, label, AppCategory.OTHER, MatchType.DEFAULT)
    }

    /**
     * Classify a batch of installed apps.
     *
     * @param apps  List of (packageName, label) pairs.
     * @return Classified results sorted by category ordinal, then label.
     */
    fun classifyAll(
        apps: List<Pair<String, String>>,
    ): List<ClassifiedApp> =
        apps.map { (pkg, label) -> classify(pkg, label) }
            .sortedWith(compareBy({ it.category.ordinal }, { it.label.lowercase() }))

    /**
     * Group classified apps by their category.
     */
    fun groupByCategory(
        apps: List<ClassifiedApp>,
    ): Map<AppCategory, List<ClassifiedApp>> =
        apps.groupBy { it.category }
}
