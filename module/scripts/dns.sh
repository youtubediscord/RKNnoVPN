#!/system/bin/sh
# ============================================================================
# dns.sh — DNS interception for PrivStack
# ============================================================================
# Transparently hijacks DNS traffic so the proxy core resolves everything.
#
# Two interception layers:
#   1. Port 53 (classic DNS)  — redirected via iptables nat REDIRECT
#   2. DoT (853)              — intercepted via mangle TPROXY
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
#   PRIVSTACK_DIR  — base directory, e.g. /data/adb/privstack
# ============================================================================

# Fail on first error; treat unset variables as errors.
set -eu

TAG="privstack:dns"

# Sane defaults if the caller omitted something.
DNS_PORT="${DNS_PORT:-10856}"
CORE_GID="${CORE_GID:-23333}"
DNS_MODE="${DNS_MODE:-uid}"
APP_UIDS="${APP_UIDS:-}"
PRIVSTACK_DIR="${PRIVSTACK_DIR:-/data/adb/privstack}"

# Chain names — keep in sync with the rest of PrivStack.
NAT4_CHAIN="PRIVSTACK_DNS_NAT"
NAT6_CHAIN="PRIVSTACK_DNS_NAT6"
MAN4_CHAIN="PRIVSTACK_DNS_MAN"
MAN6_CHAIN="PRIVSTACK_DNS_MAN6"

log() { /system/bin/log -t "$TAG" -p i "$*"; }

# ── helpers ─────────────────────────────────────────────────────────────────

create_chains() {
    # IPv4
    iptables  -t nat    -N "$NAT4_CHAIN" 2>/dev/null || iptables  -t nat    -F "$NAT4_CHAIN"
    iptables  -t mangle -N "$MAN4_CHAIN" 2>/dev/null || iptables  -t mangle -F "$MAN4_CHAIN"
    # IPv6
    ip6tables -t nat    -N "$NAT6_CHAIN" 2>/dev/null || ip6tables -t nat    -F "$NAT6_CHAIN"
    ip6tables -t mangle -N "$MAN6_CHAIN" 2>/dev/null || ip6tables -t mangle -F "$MAN6_CHAIN"
}

# Populate a single nat chain ($1=iptables/ip6tables, $2=chain).
fill_nat_chain() {
    _ipt="$1"; _chain="$2"

    # Always skip the proxy core's own traffic to avoid loops.
    $_ipt -t nat -A "$_chain" -m owner --gid-owner "$CORE_GID" -j RETURN

    if [ "$DNS_MODE" = "all" ]; then
        # Redirect every UDP/TCP 53 packet to the core's DNS listener.
        $_ipt -t nat -A "$_chain" -p udp --dport 53 -j REDIRECT --to-ports "$DNS_PORT"
        $_ipt -t nat -A "$_chain" -p tcp --dport 53 -j REDIRECT --to-ports "$DNS_PORT"
    else
        # Per-UID: only redirect listed UIDs.
        for uid in $APP_UIDS; do
            $_ipt -t nat -A "$_chain" -p udp --dport 53 -m owner --uid-owner "$uid" -j REDIRECT --to-ports "$DNS_PORT"
            $_ipt -t nat -A "$_chain" -p tcp --dport 53 -m owner --uid-owner "$uid" -j REDIRECT --to-ports "$DNS_PORT"
        done
    fi
}

# Populate a single mangle chain ($1=iptables/ip6tables, $2=chain).
# TPROXY intercepts DoT (853). We intentionally do not blanket-intercept
# generic 443 traffic here because that would break normal HTTPS/QUIC apps.
fill_mangle_chain() {
    _ipt="$1"; _chain="$2"

    # Skip core's own traffic.
    $_ipt -t mangle -A "$_chain" -m owner --gid-owner "$CORE_GID" -j RETURN

    if [ "$DNS_MODE" = "all" ]; then
        # DoT — TCP 853
        $_ipt -t mangle -A "$_chain" -p tcp --dport 853 \
              -j TPROXY --on-port "$DNS_PORT" --tproxy-mark 0x2023
    else
        for uid in $APP_UIDS; do
            $_ipt -t mangle -A "$_chain" -p tcp --dport 853 \
                  -m owner --uid-owner "$uid" \
                  -j TPROXY --on-port "$DNS_PORT" --tproxy-mark 0x2023
        done
    fi
}

# Hook our chains into OUTPUT so locally-generated traffic is caught.
hook_chains() {
    # nat OUTPUT — classic DNS redirect
    iptables  -t nat    -C OUTPUT -j "$NAT4_CHAIN" 2>/dev/null || \
        iptables  -t nat    -A OUTPUT -j "$NAT4_CHAIN"
    ip6tables -t nat    -C OUTPUT -j "$NAT6_CHAIN" 2>/dev/null || \
        ip6tables -t nat    -A OUTPUT -j "$NAT6_CHAIN"

    # mangle OUTPUT — TPROXY for DoT/DoH
    iptables  -t mangle -C OUTPUT -j "$MAN4_CHAIN" 2>/dev/null || \
        iptables  -t mangle -A OUTPUT -j "$MAN4_CHAIN"
    ip6tables -t mangle -C OUTPUT -j "$MAN6_CHAIN" 2>/dev/null || \
        ip6tables -t mangle -A OUTPUT -j "$MAN6_CHAIN"
}

# ── start ───────────────────────────────────────────────────────────────────

start() {
    log "starting DNS interception (mode=$DNS_MODE, port=$DNS_PORT)"

    create_chains

    # Fill nat chains (port 53 redirect).
    fill_nat_chain iptables  "$NAT4_CHAIN"
    fill_nat_chain ip6tables "$NAT6_CHAIN"

    # Fill mangle chains (DoT/DoH TPROXY).
    fill_mangle_chain iptables  "$MAN4_CHAIN"
    fill_mangle_chain ip6tables "$MAN6_CHAIN"

    # Attach to OUTPUT.
    hook_chains

    log "DNS interception started"
}

# ── stop ────────────────────────────────────────────────────────────────────

stop() {
    log "stopping DNS interception"

    # Unhook from OUTPUT (ignore errors if already removed).
    iptables  -t nat    -D OUTPUT -j "$NAT4_CHAIN" 2>/dev/null || true
    ip6tables -t nat    -D OUTPUT -j "$NAT6_CHAIN" 2>/dev/null || true
    iptables  -t mangle -D OUTPUT -j "$MAN4_CHAIN" 2>/dev/null || true
    ip6tables -t mangle -D OUTPUT -j "$MAN6_CHAIN" 2>/dev/null || true

    # Flush and delete chains.
    iptables  -t nat    -F "$NAT4_CHAIN" 2>/dev/null || true
    iptables  -t nat    -X "$NAT4_CHAIN" 2>/dev/null || true
    ip6tables -t nat    -F "$NAT6_CHAIN" 2>/dev/null || true
    ip6tables -t nat    -X "$NAT6_CHAIN" 2>/dev/null || true
    iptables  -t mangle -F "$MAN4_CHAIN" 2>/dev/null || true
    iptables  -t mangle -X "$MAN4_CHAIN" 2>/dev/null || true
    ip6tables -t mangle -F "$MAN6_CHAIN" 2>/dev/null || true
    ip6tables -t mangle -X "$MAN6_CHAIN" 2>/dev/null || true

    # NOTE: We never touched Private DNS, so there is nothing to restore.

    log "DNS interception stopped"
}

# ── main dispatch ───────────────────────────────────────────────────────────

case "${1:-}" in
    start) start ;;
    stop)  stop  ;;
    *)     echo "Usage: $0 {start|stop}" >&2; exit 1 ;;
esac
