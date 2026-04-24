package com.privstack.panel.advisor

/**
 * Built-in package policy for apps that should stay outside PrivStack even
 * when the rest of the device is proxied.
 */
object AlwaysDirectApps {
    private val exactPackages = setOf(
        "ru.oneme.app",
        "ru.vtb24.mobilebanking.android",
        "com.avito.android",
        "ru.ozon.app.android",
        "com.wildberries.ru",
        "ru.beru.android",
        "ru.yandex.taxi",
        "ru.yandex.yandexmaps",
        "ru.yandex.searchplugin",
        "ru.yandex.browser",
        "ru.yandex.browser.lite",
        "ru.yandex.music",
        "ru.yandex.disk",
        "ru.yandex.mail",
        "ru.yandex.market",
        "ru.yandex.metro",
        "ru.yandex.weatherplugin",
        "ru.yandex.mobile.auth",
        "ru.sberbankmobile",
        "ru.sberbankmobile.arm",
        "ru.alfabank.mobile.android",
        "ru.tinkoff.android",
        "ru.tinkoff.investing",
        "com.idamob.tinkoff.android",
        "ru.raiffeisennews",
        "ru.rosbank.android",
        "ru.psbank.online",
        "ru.mts.bank",
        "ru.gosuslugi.pos",
        "ru.fns.lkfl",
        "ru.nalog.ibr",
        "ru.mos.app",
        "ru.mts.mymts",
        "com.beeline.dc",
        "ru.megafon.mlk",
        "ru.tele2.mytele2",
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
        "moe.nb4a",
        "org.outline.android.client",
        "net.openvpn.openvpn",
        "de.blinkt.openvpn",
        "com.github.shadowsocks",
        "com.getsurfboard",
        "com.github.kr328.clash",
        "com.github.metacubex.clash.meta",
        "com.notcvnt.rknhardering",
        "com.yourvpndead",
    )

    private val prefixes = listOf(
        "ru.yandex.",
        "com.yandex.",
        "ru.vtb",
        "ru.sber",
        "ru.alfabank",
        "ru.tinkoff",
        "com.idamob.tinkoff",
        "ru.raiffeisen",
        "ru.rosbank",
        "ru.psbank",
        "ru.mts.bank",
        "ru.gosuslugi",
        "ru.fns",
        "ru.nalog",
        "ru.mos",
        "com.avito",
        "ru.ozon",
        "com.wildberries",
    )

    private val keywords = listOf(
        "vpn",
        "proxy",
        "v2ray",
        "xray",
        "hiddify",
        "nekobox",
        "nekoray",
        "amnezia",
        "wireguard",
        "outline",
        "openvpn",
        "shadowsocks",
        "clash",
        "singbox",
        "sing-box",
        "sagernet",
        "tun2socks",
    )

    fun matches(packageName: String, manualPackages: Set<String> = emptySet()): Boolean {
        if (packageName in manualPackages || packageName in exactPackages) return true
        if (prefixes.any(packageName::startsWith)) return true
        val lower = packageName.lowercase()
        return keywords.any(lower::contains)
    }
}
