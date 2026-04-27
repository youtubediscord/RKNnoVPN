#!/system/bin/sh
# ============================================================================
# routing.sh — Policy routing setup for RKNnoVPN
# ============================================================================
# Creates the ip-rule and ip-route entries that make TPROXY work.
#
# TPROXY-marked packets need to be routed to the local machine so the proxy
# core can pick them up.  This script adds:
#   - ip rule:  fwmark $FWMARK → lookup table $ROUTE_TABLE
#   - ip route: local 0.0.0.0/0 dev lo  (in that table)
#   - Same pair for IPv6.
#   - Enables ip_forward (v4 + v6) so forwarded packets are not dropped.
#   - Disables rp_filter (reverse-path filtering) which rejects TPROXY
#     packets because their source address does not match the receiving
#     interface.
#
# Environment (set by daemon):
#   FWMARK         — hex mark, e.g. 0x2023
#   ROUTE_TABLE    — IPv4 table number, e.g. 2023
#   ROUTE_TABLE_V6 — IPv6 table number, e.g. 2024
# ============================================================================

set -eu

TAG="rknnovpn:routing"
SCRIPT_DIR="${0%/*}"
if [ -f "${SCRIPT_DIR}/lib/rknnovpn_env.sh" ]; then
    . "${SCRIPT_DIR}/lib/rknnovpn_env.sh"
fi
if [ -f "${SCRIPT_DIR}/lib/rknnovpn_netstack.sh" ]; then
    . "${SCRIPT_DIR}/lib/rknnovpn_netstack.sh"
fi

FWMARK="${FWMARK:-0x2023}"
ROUTE_TABLE="${ROUTE_TABLE:-2023}"
ROUTE_TABLE_V6="${ROUTE_TABLE_V6:-2024}"

log() { /system/bin/log -t "$TAG" -p i "$*"; }

# ── start ───────────────────────────────────────────────────────────────────

start() {
    log "setting up policy routing (mark=$FWMARK, table=$ROUTE_TABLE/$ROUTE_TABLE_V6)"

    if command -v rknnovpn_delete_policy_routes >/dev/null 2>&1; then
        rknnovpn_delete_policy_routes
    else
        ip rule del fwmark "$FWMARK" table "$ROUTE_TABLE" 2>/dev/null || true
        ip -6 rule del fwmark "$FWMARK" table "$ROUTE_TABLE_V6" 2>/dev/null || true
    fi

    # ── IPv4 ────────────────────────────────────────────────────────────
    ip rule add fwmark "$FWMARK" table "$ROUTE_TABLE" pref 100 2>/dev/null || true
    # Local catch-all route — TPROXY packets are delivered to lo.
    ip route add local 0.0.0.0/0 dev lo table "$ROUTE_TABLE" 2>/dev/null || \
        ip route replace local 0.0.0.0/0 dev lo table "$ROUTE_TABLE"

    # ── IPv6 ────────────────────────────────────────────────────────────
    ip -6 rule add fwmark "$FWMARK" table "$ROUTE_TABLE_V6" pref 100 2>/dev/null || true
    ip -6 route add local ::/0 dev lo table "$ROUTE_TABLE_V6" 2>/dev/null || \
        ip -6 route replace local ::/0 dev lo table "$ROUTE_TABLE_V6"

    # ── IP forwarding ───────────────────────────────────────────────────
    # Required for tethering and forwarded packets to traverse TPROXY.
    [ -w /proc/sys/net/ipv4/ip_forward ] && echo 1 > /proc/sys/net/ipv4/ip_forward
    [ -w /proc/sys/net/ipv6/conf/all/forwarding ] && echo 1 > /proc/sys/net/ipv6/conf/all/forwarding

    # ── Reverse-path filter ─────────────────────────────────────────────
    # TPROXY packets arrive with a foreign source address on lo, which
    # rp_filter rightfully considers bogus.  Disable it.
    [ -w /proc/sys/net/ipv4/conf/all/rp_filter ] && echo 0 > /proc/sys/net/ipv4/conf/all/rp_filter
    [ -w /proc/sys/net/ipv4/conf/default/rp_filter ] && echo 0 > /proc/sys/net/ipv4/conf/default/rp_filter
    # Also disable on every existing interface to be safe.
    for f in /proc/sys/net/ipv4/conf/*/rp_filter; do
        [ -w "$f" ] && echo 0 > "$f"
    done

    log "policy routing ready"
}

# ── stop ────────────────────────────────────────────────────────────────────

stop() {
    log "tearing down policy routing"

    if command -v rknnovpn_delete_policy_routes >/dev/null 2>&1; then
        rknnovpn_delete_policy_routes
    else
        ip rule del fwmark "$FWMARK" table "$ROUTE_TABLE" 2>/dev/null || true
        ip route flush table "$ROUTE_TABLE" 2>/dev/null || true
        ip -6 rule del fwmark "$FWMARK" table "$ROUTE_TABLE_V6" 2>/dev/null || true
        ip -6 route flush table "$ROUTE_TABLE_V6" 2>/dev/null || true
    fi

    # NOTE: We intentionally do NOT restore ip_forward or rp_filter.
    # Other subsystems (tethering, hotspot) may depend on forwarding being
    # enabled, and toggling rp_filter mid-flight can cause brief
    # connectivity drops.  The kernel defaults are restored on reboot.

    log "policy routing torn down"
}

# ── main dispatch ───────────────────────────────────────────────────────────

case "${1:-}" in
    start) start ;;
    stop)  stop  ;;
    *)     echo "Usage: $0 {start|stop}" >&2; exit 1 ;;
esac
