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
# Compatibility handler for older inotifyd-based launches.
#
# Current privd owns network-change reconciliation in Go and re-applies the
# complete netstack through scripts/iptables.sh + scripts/dns.sh. This script
# must not partially mutate PRIVSTACK_* chains: doing so can erase reserved
# bypass rules or create policy-routing drift.
#
# Environment (set by privd):
#   PRIVSTACK_DIR  — base directory, e.g. /data/adb/privstack
#   FWMARK         — packet mark used for policy routing (hex, e.g. 0x2023)
#   ROUTE_TABLE    — IPv4 policy routing table number
#   ROUTE_TABLE_V6 — IPv6 policy routing table number
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
IPT_WAIT="${IPT_WAIT:--w 30}"

LOG_FILE="$LOG_DIR/net_change.log"
LOCK_FILE="$RUN_DIR/net_change.lock"

# inotifyd arguments.
EVENT="${1:-unknown}"
MON_DIR="${2:-}"
MON_FILE="${3:-}"

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
log "legacy handler observed event: dir=$MON_DIR file=$MON_FILE"
log "netstack reconciliation is daemon-owned; no direct rule mutation performed"

if ip link show tun0 >/dev/null 2>&1; then
    log "WARNING: tun0 detected - a VPN is active. TPROXY and VPN may conflict."
fi

log "legacy network handler complete"
