#!/system/bin/sh
# ============================================================================
# net_handler.sh — Network change handler for PrivStack
# ============================================================================
# Called by inotifyd when files under /data/misc/net/ change.
#
# Invocation:  net_handler.sh EVENT MONITORED_DIR MONITORED_FILE
#   EVENT          — inotifyd event letter (c=create, d=delete, w=write, …)
#   MONITORED_DIR  — directory being watched
#   MONITORED_FILE — file that triggered the event
#
# Responsibilities:
#   1. Debounce rapid-fire events (2-second lock file).
#   2. Rebuild the PRIVSTACK_BYPASS chain with current local addresses.
#   3. Verify core ip-rule / iptables hooks are still intact.
#   4. Detect new tethering interfaces and add TPROXY rules for them.
#   5. Warn if a VPN (tun0) appears — it conflicts with TPROXY.
#   6. Log everything to $PRIVSTACK_DIR/logs/net_change.log.
#
# Environment (set by privd):
#   PRIVSTACK_DIR  — base directory, e.g. /data/adb/privstack
#   FWMARK         — packet mark used for policy routing (hex, e.g. 0x2023)
#   ROUTE_TABLE    — IPv4 policy routing table number
#   ROUTE_TABLE_V6 — IPv6 policy routing table number
#   TPROXY_PORT    — sing-box TPROXY listener port
#   SHARING_MODE   — off | hotspot (default off)
#   SHARING_IFACES — optional explicit tethering interface names
# ============================================================================

set -eu

TAG="privstack:net"

# Defaults matching config.json.
PRIVSTACK_DIR="${PRIVSTACK_DIR:-/data/adb/privstack}"
SCRIPT_DIR="${0%/*}"
if [ -f "${SCRIPT_DIR}/lib/privstack_env.sh" ]; then
    . "${SCRIPT_DIR}/lib/privstack_env.sh"
fi
FWMARK="${FWMARK:-0x2023}"
ROUTE_TABLE="${ROUTE_TABLE:-2023}"
ROUTE_TABLE_V6="${ROUTE_TABLE_V6:-2024}"
TPROXY_PORT="${TPROXY_PORT:-10853}"
SHARING_MODE="${SHARING_MODE:-off}"
SHARING_IFACES="${SHARING_IFACES:-}"
IPT_WAIT="${IPT_WAIT:--w 30}"

LOG_FILE="$LOG_DIR/net_change.log"
LOCK_FILE="$RUN_DIR/net_change.lock"

# inotifyd arguments.
EVENT="${1:-unknown}"
MON_DIR="${2:-}"
MON_FILE="${3:-}"

# Chain names — keep in sync with the main firewall script.
BYPASS4_CHAIN="PRIVSTACK_BYPASS"
BYPASS6_CHAIN="PRIVSTACK_BYPASS"
PRE4_CHAIN="PRIVSTACK_PRE"
PRE6_CHAIN="PRIVSTACK_PRE"

# ── logging ─────────────────────────────────────────────────────────────────

log() {
    _ts=$(date '+%Y-%m-%d %H:%M:%S' 2>/dev/null || echo "???")
    _msg="$_ts [$EVENT] $*"
    echo "$_msg" >> "$LOG_FILE"
    /system/bin/log -t "$TAG" -p i "$*"
}

# Ensure log directory exists.
mkdir -p "$(dirname "$LOG_FILE")" 2>/dev/null || true
mkdir -p "$(dirname "$LOCK_FILE")" 2>/dev/null || true

# A network event must never resurrect proxy rules while reset is in progress
# or after the user explicitly disabled automatic runtime startup.
if privstack_is_reset_active; then
    log "reset lock present - exiting"
    exit 0
fi

if ! privstack_is_active; then
    log "PrivStack is not active - exiting"
    exit 0
fi

if privstack_is_manual_mode; then
    log "manual mode enabled - exiting"
    exit 0
fi

# ── debounce ────────────────────────────────────────────────────────────────
# Network changes come in bursts (Wi-Fi reassociation fires 4-6 events in
# under a second).  Use a lock file with a 2-second expiry.

acquire_lock() {
    if [ -f "$LOCK_FILE" ]; then
        _age=$(( $(date +%s) - $(stat -c %Y "$LOCK_FILE" 2>/dev/null || echo 0) ))
        if [ "$_age" -lt 2 ]; then
            log "debounce: skipping (lock age=${_age}s)"
            exit 0
        fi
    fi
    # Create / refresh the lock.
    : > "$LOCK_FILE"
}

acquire_lock
log "handling event: dir=$MON_DIR file=$MON_FILE"

# ── 1. Rebuild PRIVSTACK_BYPASS with current local IPs ─────────────────────
# Traffic destined for local addresses must skip the proxy.

rebuild_bypass() {
    log "rebuilding bypass chains with current local addresses"

    # IPv4 ────────────────────────────────────────────────────────────────
    iptables $IPT_WAIT -t mangle -F "$BYPASS4_CHAIN" 2>/dev/null || \
        iptables $IPT_WAIT -t mangle -N "$BYPASS4_CHAIN"

    # Always bypass loopback and link-local. ACCEPT is terminal for the
    # current table; RETURN would resume PRIVSTACK_PRE/OUT after the bypass
    # jump and could still hit marking/TPROXY rules.
    iptables $IPT_WAIT -t mangle -A "$BYPASS4_CHAIN" -d 127.0.0.0/8   -j ACCEPT
    iptables $IPT_WAIT -t mangle -A "$BYPASS4_CHAIN" -d 169.254.0.0/16 -j ACCEPT

    # Add every address currently assigned to an interface.
    ip -4 addr show 2>/dev/null | awk '/inet / { print $2 }' | while read -r cidr; do
        iptables $IPT_WAIT -t mangle -A "$BYPASS4_CHAIN" -d "$cidr" -j ACCEPT
        log "  bypass4: $cidr"
    done

    # IPv6 ────────────────────────────────────────────────────────────────
    ip6tables $IPT_WAIT -t mangle -F "$BYPASS6_CHAIN" 2>/dev/null || \
        ip6tables $IPT_WAIT -t mangle -N "$BYPASS6_CHAIN"

    # Always bypass loopback.
    ip6tables $IPT_WAIT -t mangle -A "$BYPASS6_CHAIN" -d ::1/128 -j ACCEPT

    # Add non-link-local, non-loopback addresses.
    ip -6 addr show 2>/dev/null | awk '/inet6 / { print $2 }' | \
        grep -vE '^fe80|^::1' | while read -r cidr; do
        ip6tables $IPT_WAIT -t mangle -A "$BYPASS6_CHAIN" -d "$cidr" -j ACCEPT
        log "  bypass6: $cidr"
    done

    log "bypass chains rebuilt"
}

rebuild_bypass

# ── 2. Verify core policy-routing rules ─────────────────────────────────────
# A connectivity change can flush ip rules on some ROMs.  Re-add if missing.

verify_ip_rules() {
    log "verifying ip rules"

    # IPv4 fwmark rule.
    if ! ip rule show 2>/dev/null | grep -q "fwmark $FWMARK"; then
        ip rule add fwmark "$FWMARK" table "$ROUTE_TABLE" pref 100
        log "  re-added IPv4 fwmark rule (table $ROUTE_TABLE)"
    fi

    # IPv4 local route.
    if ! ip route show table "$ROUTE_TABLE" 2>/dev/null | grep -q "local"; then
        ip route add local 0.0.0.0/0 dev lo table "$ROUTE_TABLE"
        log "  re-added IPv4 local route (table $ROUTE_TABLE)"
    fi

    # IPv6 fwmark rule.
    if ! ip -6 rule show 2>/dev/null | grep -q "fwmark $FWMARK"; then
        ip -6 rule add fwmark "$FWMARK" table "$ROUTE_TABLE_V6" pref 100
        log "  re-added IPv6 fwmark rule (table $ROUTE_TABLE_V6)"
    fi

    # IPv6 local route.
    if ! ip -6 route show table "$ROUTE_TABLE_V6" 2>/dev/null | grep -q "local"; then
        ip -6 route add local ::/0 dev lo table "$ROUTE_TABLE_V6"
        log "  re-added IPv6 local route (table $ROUTE_TABLE_V6)"
    fi
}

verify_ip_rules

# ── 3. Verify iptables hooks ───────────────────────────────────────────────
# Make sure our jump into PREROUTING is still present.

verify_iptables_hooks() {
    log "verifying iptables hooks"

    # IPv4 mangle PREROUTING.
    if ! iptables $IPT_WAIT -t mangle -C PREROUTING -j "$PRE4_CHAIN" 2>/dev/null; then
        iptables $IPT_WAIT -t mangle -A PREROUTING -j "$PRE4_CHAIN"
        log "  re-hooked $PRE4_CHAIN into PREROUTING (IPv4)"
    fi

    # IPv6 mangle PREROUTING.
    if ! ip6tables $IPT_WAIT -t mangle -C PREROUTING -j "$PRE6_CHAIN" 2>/dev/null; then
        ip6tables $IPT_WAIT -t mangle -A PREROUTING -j "$PRE6_CHAIN"
        log "  re-hooked $PRE6_CHAIN into PREROUTING (IPv6)"
    fi
}

verify_iptables_hooks

# ── 4. Detect new tethering interfaces ─────────────────────────────────────
# Common tethering interfaces: ap0, wlan1, rndis0, usb0, bt-pan, swlan0.
# If one appears, ensure TPROXY rules cover traffic arriving on it.

handle_tethering() {
    if [ "$SHARING_MODE" != "hotspot" ]; then
        log "sharing mode disabled; skipping tethering TPROXY rules"
        return
    fi
    log "checking for tethering interfaces"

    ifaces="${SHARING_IFACES:-$(ip -o link show 2>/dev/null | awk -F': ' '{ print $2 }' | \
                   grep -E '^(ap[0-9]|wlan[1-9]|rndis[0-9]|usb[0-9]|bt-pan|swlan[0-9])' || true)}"

    for iface in $ifaces; do

        # Skip if a rule for this interface already exists.
        if iptables $IPT_WAIT -t mangle -C "$PRE4_CHAIN" -i "$iface" \
              -p tcp -j TPROXY --on-port "$TPROXY_PORT" --tproxy-mark "$FWMARK" 2>/dev/null; then
            continue
        fi

        log "  adding TPROXY rules for tethering iface: $iface"

        # IPv4 TCP + UDP.
        iptables $IPT_WAIT -t mangle -A "$PRE4_CHAIN" -i "$iface" -p tcp \
            -j TPROXY --on-port "$TPROXY_PORT" --tproxy-mark "$FWMARK"
        iptables $IPT_WAIT -t mangle -A "$PRE4_CHAIN" -i "$iface" -p udp \
            -j TPROXY --on-port "$TPROXY_PORT" --tproxy-mark "$FWMARK"

        # IPv6 TCP + UDP.
        ip6tables $IPT_WAIT -t mangle -A "$PRE6_CHAIN" -i "$iface" -p tcp \
            -j TPROXY --on-port "$TPROXY_PORT" --tproxy-mark "$FWMARK"
        ip6tables $IPT_WAIT -t mangle -A "$PRE6_CHAIN" -i "$iface" -p udp \
            -j TPROXY --on-port "$TPROXY_PORT" --tproxy-mark "$FWMARK"
    done
}

handle_tethering

# ── 5. VPN conflict detection ──────────────────────────────────────────────
# If a VPN app creates tun0, our TPROXY rules and the VPN will fight over
# routing.  We log a prominent warning but do NOT tear down — the user may
# have intentionally started a VPN for a specific app.

check_vpn_conflict() {
    if ip link show tun0 >/dev/null 2>&1; then
        log "WARNING: tun0 detected — a VPN is active. TPROXY and VPN may conflict."
    fi
}

check_vpn_conflict

log "network change handling complete"
