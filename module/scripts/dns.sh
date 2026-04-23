#!/system/bin/sh
# ============================================================================
# dns.sh — DNS interception for PrivStack
# ============================================================================
# Transparently redirects classic DNS traffic so the proxy core resolves it.
#
# Interception layer:
#   - Port 53 (classic DNS) — redirected via iptables nat REDIRECT.
#   - DoT/DoH are ordinary TCP/HTTPS traffic and are handled by the main
#     TPROXY path for selected apps. TPROXY is not valid from mangle OUTPUT
#     on typical Android/Linux iptables, so this script must not install
#     separate OUTPUT TPROXY rules.
#
# IMPORTANT: We never touch Settings.Global (Private DNS).  Apps can detect
#            that change and refuse to work.  Instead we intercept at the
#            network layer, which is invisible to userspace.
#
# Environment (set by privd before calling this script):
#   DNS_PORT       — sing-box DNS listener port (default 10856)
#   CORE_GID       — GID used by the proxy core (default 23333)
#   DNS_MODE       — "uid" = per-UID interception; "all" = everything
#   APP_UIDS       — space-separated list of UIDs to intercept (uid mode)
#   BYPASS_UIDS    — space-separated list of UIDs that must never be redirected
#   PRIVSTACK_DIR  — base directory, e.g. /data/adb/privstack
# ============================================================================

# Fail on first error; treat unset variables as errors.
set -eu

TAG="privstack:dns"
SCRIPT_VERSION="v1.6.2"

# Sane defaults if the caller omitted something.
DNS_PORT="${DNS_PORT:-10856}"
CORE_GID="${CORE_GID:-23333}"
DNS_MODE="${DNS_MODE:-uid}"
APP_UIDS="${APP_UIDS:-}"
BYPASS_UIDS="${BYPASS_UIDS:-}"
PRIVSTACK_DIR="${PRIVSTACK_DIR:-/data/adb/privstack}"
IPT_WAIT="-w 100"

# Chain names — keep in sync with the rest of PrivStack.
NAT4_CHAIN="PRIVSTACK_DNS_NAT"
NAT6_CHAIN="PRIVSTACK_DNS_NAT6"
HAS_IPV6_NAT=0

log() { /system/bin/log -t "$TAG" -p i "$*"; }

# ── helpers ─────────────────────────────────────────────────────────────────

has_ipv6_nat() {
    ip6tables $IPT_WAIT -t nat -L >/dev/null 2>&1
}

create_chains() {
    # IPv4
    iptables  $IPT_WAIT -t nat -N "$NAT4_CHAIN" 2>/dev/null || iptables  $IPT_WAIT -t nat -F "$NAT4_CHAIN"
    # IPv6
    if has_ipv6_nat; then
        HAS_IPV6_NAT=1
        ip6tables $IPT_WAIT -t nat -N "$NAT6_CHAIN" 2>/dev/null || ip6tables $IPT_WAIT -t nat -F "$NAT6_CHAIN"
    else
        HAS_IPV6_NAT=0
        log "IPv6 nat table is unavailable; skipping IPv6 DNS interception"
    fi
}

# Populate a single nat chain ($1=iptables/ip6tables, $2=chain).
fill_nat_chain() {
    _ipt="$1"; _chain="$2"

    # Always skip the proxy core's own traffic to avoid loops.
    $_ipt $IPT_WAIT -t nat -A "$_chain" -m owner --gid-owner "$CORE_GID" -j RETURN

    # Always-direct apps must not have their classic DNS redirected either.
    for uid in $BYPASS_UIDS; do
        $_ipt $IPT_WAIT -t nat -A "$_chain" -m owner --uid-owner "$uid" -j RETURN
    done

    if [ "$DNS_MODE" = "all" ]; then
        # Redirect every UDP/TCP 53 packet to the core's DNS listener.
        $_ipt $IPT_WAIT -t nat -A "$_chain" -p udp --dport 53 -j REDIRECT --to-ports "$DNS_PORT"
        $_ipt $IPT_WAIT -t nat -A "$_chain" -p tcp --dport 53 -j REDIRECT --to-ports "$DNS_PORT"
    elif [ "$DNS_MODE" = "per_uid" ] || [ "$DNS_MODE" = "uid" ]; then
        # Per-UID: only redirect listed UIDs.
        for uid in $APP_UIDS; do
            $_ipt $IPT_WAIT -t nat -A "$_chain" -p udp --dport 53 -m owner --uid-owner "$uid" -j REDIRECT --to-ports "$DNS_PORT"
            $_ipt $IPT_WAIT -t nat -A "$_chain" -p tcp --dport 53 -m owner --uid-owner "$uid" -j REDIRECT --to-ports "$DNS_PORT"
        done
    else
        log "DNS redirect disabled (mode=$DNS_MODE)"
    fi
}

# Hook our chains into OUTPUT so locally-generated traffic is caught.
hook_chains() {
    # nat OUTPUT — classic DNS redirect
    iptables  $IPT_WAIT -t nat -C OUTPUT -j "$NAT4_CHAIN" 2>/dev/null || \
        iptables  $IPT_WAIT -t nat -A OUTPUT -j "$NAT4_CHAIN"
    if [ "$HAS_IPV6_NAT" -eq 1 ]; then
        ip6tables $IPT_WAIT -t nat -C OUTPUT -j "$NAT6_CHAIN" 2>/dev/null || \
            ip6tables $IPT_WAIT -t nat -A OUTPUT -j "$NAT6_CHAIN"
    fi
}

# ── start ───────────────────────────────────────────────────────────────────

start() {
    log "starting DNS interception $SCRIPT_VERSION (mode=$DNS_MODE, port=$DNS_PORT)"

    if [ "$DNS_MODE" = "off" ]; then
        stop
        log "DNS interception disabled"
        return
    fi

    create_chains

    # Fill nat chains (port 53 redirect).
    fill_nat_chain iptables  "$NAT4_CHAIN"
    if [ "$HAS_IPV6_NAT" -eq 1 ]; then
        fill_nat_chain ip6tables "$NAT6_CHAIN"
    fi

    # Attach to OUTPUT.
    hook_chains

    log "DNS interception started"
}

# ── stop ────────────────────────────────────────────────────────────────────

stop() {
    log "stopping DNS interception"

    # Unhook from OUTPUT (ignore errors if already removed).
    iptables  $IPT_WAIT -t nat -D OUTPUT -j "$NAT4_CHAIN" 2>/dev/null || true
    if has_ipv6_nat; then
        ip6tables $IPT_WAIT -t nat -D OUTPUT -j "$NAT6_CHAIN" 2>/dev/null || true
    fi

    # Flush and delete chains.
    iptables  $IPT_WAIT -t nat -F "$NAT4_CHAIN" 2>/dev/null || true
    iptables  $IPT_WAIT -t nat -X "$NAT4_CHAIN" 2>/dev/null || true
    if has_ipv6_nat; then
        ip6tables $IPT_WAIT -t nat -F "$NAT6_CHAIN" 2>/dev/null || true
        ip6tables $IPT_WAIT -t nat -X "$NAT6_CHAIN" 2>/dev/null || true
    fi

    # NOTE: We never touched Private DNS, so there is nothing to restore.

    log "DNS interception stopped"
}

# ── main dispatch ───────────────────────────────────────────────────────────

case "${1:-}" in
    start) start ;;
    stop)  stop  ;;
    *)     echo "Usage: $0 {start|stop}" >&2; exit 1 ;;
esac
