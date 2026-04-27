package com.rknnovpn.panel.advisor

/**
 * A pre-built whitelist template grouping package names by purpose.
 *
 * @param id            Stable machine identifier.
 * @param displayName   Human-readable label.
 * @param packages      Package names included in this template.
 * @param isInverse     When true, the template means "all apps EXCEPT [packages]".
 */
data class Template(
    val id: String,
    val displayName: String,
    val packages: Set<String>,
    val isInverse: Boolean = false,
) {
    /**
     * Check whether [packageName] is covered by this template.
     * For inverse templates, covered means the package is NOT in [packages].
     */
    fun covers(packageName: String): Boolean =
        if (isInverse) packageName !in packages else packageName in packages
}

/**
 * Pre-built whitelist templates for common app-routing scenarios.
 *
 * Each template contains a curated set of package names that can be applied
 * to per-app routing rules in a single tap.
 */
object AppTemplates {

    val BROWSERS = Template(
        id = "browsers",
        displayName = "Browsers",
        packages = setOf(
            "com.android.chrome",
            "org.mozilla.firefox",
            "org.mozilla.firefox_beta",
            "org.mozilla.fenix",
            "com.microsoft.emmx",
            "com.sec.android.app.sbrowser",
            "com.yandex.browser",
            "com.yandex.browser.lite",
            "com.opera.browser",
            "com.opera.mini.native",
            "com.brave.browser",
            "com.duckduckgo.mobile.android",
            "com.vivaldi.browser",
            "com.kiwibrowser.browser",
            "com.UCMobile.intl",
        ),
    )

    val SOCIAL_MESSAGING = Template(
        id = "social_messaging",
        displayName = "Social / Messaging",
        packages = setOf(
            "org.telegram.messenger",
            "org.telegram.messenger.web",
            "org.thunderdog.chalern",
            "com.whatsapp",
            "com.whatsapp.w4b",
            "org.thoughtcrime.securesms",
            "com.discord",
            "com.vkontakte.android",
            "com.instagram.android",
            "com.facebook.orca",
            "com.facebook.katana",
            "com.snapchat.android",
            "com.viber.voip",
            "com.twitter.android",
            "com.zhiliaoapp.musically",
            "com.reddit.frontpage",
            "jp.naver.line.android",
            "com.tencent.mm",
        ),
    )

    val STREAMING = Template(
        id = "streaming",
        displayName = "Streaming",
        packages = setOf(
            "com.google.android.youtube",
            "com.netflix.mediaclient",
            "tv.twitch.android.app",
            "com.spotify.music",
            "com.amazon.avod.thirdpartyclient",
            "com.disney.disneyplus",
            "ru.more.play",
            "com.apple.android.music",
            "ru.kinopoisk",
        ),
    )

    val BANKING = Template(
        id = "banking",
        displayName = "Banking",
        packages = setOf(
            "ru.sberbankmobile",
            "ru.sberbankmobile.arm",
            "ru.alfabank.mobile.android",
            "ru.tinkoff.investing",
            "ru.tinkoff.android",
            "com.idamob.tinkoff.android",
            "ru.vtb24.mobilebanking.android",
            "ru.raiffeisennews",
            "ru.rosbank.android",
            "ru.psbank.online",
            "ru.mts.bank",
        ),
    )

    val GOVERNMENT = Template(
        id = "government",
        displayName = "Government",
        packages = setOf(
            "ru.gosuslugi.pos",
            "ru.roskazna.gmu",
            "ru.fns.lkfl",
            "ru.nalog.ibr",
            "ru.mos.app",
            "ru.pfr.pensioner",
        ),
    )

    val VPN_PROXY = Template(
        id = "vpn_proxy",
        displayName = "VPN / Proxy",
        packages = setOf(
            "com.wireguard.android",
            "org.torproject.android",
            "ch.protonvpn.android",
            "net.mullvad.mullvadvpn",
            "com.cloudflare.onedotonedotonedotone",
            "org.amnezia.vpn",
            "app.hiddify.com",
            "com.v2ray.ang",
            "io.nekohasekai.sfa",
            "io.nekohasekai.sagernet",
            "com.notcvnt.rknhardering",
            "com.yourvpndead",
        ),
    )

    /**
     * Inverse template: route everything EXCEPT banking and government apps.
     *
     * This is the recommended default for users who want broad proxy coverage
     * while keeping sensitive apps on a direct connection.
     */
    val EVERYTHING_EXCEPT_BANKS = Template(
        id = "everything_except_banks",
        displayName = "Everything except Banking & Gov",
        packages = BANKING.packages + GOVERNMENT.packages,
        isInverse = true,
    )

    /** All available templates in display order. */
    val all: List<Template> = listOf(
        BROWSERS,
        SOCIAL_MESSAGING,
        STREAMING,
        BANKING,
        GOVERNMENT,
        VPN_PROXY,
        EVERYTHING_EXCEPT_BANKS,
    )

    /**
     * Find a template by its stable [id].
     */
    fun findById(id: String): Template? = all.find { it.id == id }
}
