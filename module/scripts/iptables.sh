#!/system/bin/sh
# ============================================================================
# PrivStack — iptables.sh
# Whitelist-mode transparent proxy via TPROXY + iptables
#
# Called by privd with environment variables already set.
# Supports: start | stop
#
# Architecture:
#   All traffic -> TUN/routing -> iptables mangle PREROUTING (TPROXY)
#   Selected UIDs get their packets marked in OUTPUT, then TPROXY'd
#   in PREROUTING. DNS is redirected via nat OUTPUT REDIRECT.
#   sing-box runs under GID $CORE_GID to prevent routing loops.
#
# POSIX sh compatible. No bashisms.
# ============================================================================

set -eu

# ============================================================================
# Constants
# ============================================================================

TAG="PrivStack:iptables"
SCRIPT_VERSION="v1.6.3"

# Chain name prefix — all PrivStack chains start with this
CHAIN_PREFIX="PRIVSTACK"

# Chain names
CHAIN_OUT="${CHAIN_PREFIX}_OUT"       # mangle OUTPUT dispatcher
CHAIN_PRE="${CHAIN_PREFIX}_PRE"       # mangle PREROUTING dispatcher
CHAIN_APP="${CHAIN_PREFIX}_APP"       # UID whitelist matching
CHAIN_BYPASS="${CHAIN_PREFIX}_BYPASS" # reserved/private IP bypass
CHAIN_DNS="${CHAIN_PREFIX}_DNS"       # nat OUTPUT DNS redirect
CHAIN_DIVERT="${CHAIN_PREFIX}_DIVERT" # DIVERT optimization for established

# Runtime snapshot directory — stores generated rules and config for teardown
SNAPSHOT_DIR="${PRIVSTACK_DIR:-/data/adb/privstack}/run"

# iptables lock wait timeout in seconds (MIUI compatibility)
# MIUI's firewall service holds the xtables lock frequently;
# -w 100 means "wait up to 100 seconds for the lock"
IPT_WAIT="-w 100"

# Reserved IPv4 ranges — traffic to these never goes through the proxy.
# Covers RFC1918 private, loopback, link-local, documentation, multicast, etc.
RESERVED_IPV4="
0.0.0.0/8
10.0.0.0/8
100.64.0.0/10
127.0.0.0/8
169.254.0.0/16
172.16.0.0/12
192.0.0.0/24
192.0.2.0/24
192.88.99.0/24
192.168.0.0/16
198.18.0.0/15
198.51.100.0/24
203.0.113.0/24
224.0.0.0/3
"

# Reserved IPv6 ranges — same purpose as above for IPv6 traffic.
RESERVED_IPV6="
::1/128
fe80::/10
fc00::/7
ff00::/8
::ffff:0:0/96
"

# ============================================================================
# Logging helpers
# ============================================================================

log_info() {
    echo "[${TAG}] INFO: $*"
}

log_warn() {
    echo "[${TAG}] WARN: $*" >&2
}

log_error() {
    echo "[${TAG}] ERROR: $*" >&2
}

# ============================================================================
# Validation — make sure all required env vars are present
# ============================================================================

validate_env() {
    local missing=""

    for var in TPROXY_PORT DNS_PORT API_PORT FWMARK ROUTE_TABLE \
               ROUTE_TABLE_V6 CORE_GID APP_MODE PRIVSTACK_DIR; do
        eval "val=\${${var}:-}"
        if [ -z "$val" ]; then
            missing="${missing} ${var}"
        fi
    done

    if [ -n "$missing" ]; then
        log_error "Missing required environment variables:${missing}"
        exit 1
    fi

    # Default PROXY_MODE to tproxy if not set
    PROXY_MODE="${PROXY_MODE:-tproxy}"

    # Default DNS_MODE to per_uid if not set
    DNS_MODE="${DNS_MODE:-per_uid}"

    # APP_UIDS and BYPASS_UIDS can be empty
    APP_UIDS="${APP_UIDS:-}"
    BYPASS_UIDS="${BYPASS_UIDS:-}"
    HTTP_PORT="${HTTP_PORT:-10809}"
}

# ============================================================================
# Snapshot — save runtime config so stop can tear down cleanly
# even if the caller environment is different
# ============================================================================

save_snapshot() {
    mkdir -p "${SNAPSHOT_DIR}"

    cat > "${SNAPSHOT_DIR}/env.sh" <<SNAPSHOT_EOF
# PrivStack runtime snapshot — generated at $(date)
TPROXY_PORT=${TPROXY_PORT}
DNS_PORT=${DNS_PORT}
API_PORT=${API_PORT}
HTTP_PORT=${HTTP_PORT}
FWMARK=${FWMARK}
ROUTE_TABLE=${ROUTE_TABLE}
ROUTE_TABLE_V6=${ROUTE_TABLE_V6}
CORE_GID=${CORE_GID}
APP_MODE=${APP_MODE}
APP_UIDS="${APP_UIDS}"
BYPASS_UIDS="${BYPASS_UIDS}"
DNS_MODE=${DNS_MODE}
PROXY_MODE=${PROXY_MODE}
SNAPSHOT_EOF

    log_info "Runtime snapshot saved to ${SNAPSHOT_DIR}/env.sh"
}

load_snapshot() {
    if [ -f "${SNAPSHOT_DIR}/env.sh" ]; then
        . "${SNAPSHOT_DIR}/env.sh"
        log_info "Loaded runtime snapshot from ${SNAPSHOT_DIR}/env.sh"
        return 0
    fi
    return 1
}

# ============================================================================
# iptables-restore rule generation
# ============================================================================

# Generate IPv4 mangle table rules
# This is the heart of the tproxy setup:
#   OUTPUT  -> mark packets from whitelisted UIDs
#   PREROUTING -> TPROXY marked packets to sing-box
gen_mangle_v4() {
    cat <<MANGLE_V4_EOF
*mangle

# --- Create custom chains (flush if they exist) ---
:${CHAIN_OUT} - [0:0]
:${CHAIN_PRE} - [0:0]
:${CHAIN_APP} - [0:0]
:${CHAIN_BYPASS} - [0:0]
:${CHAIN_DIVERT} - [0:0]

# === DIVERT chain ===
# Optimization: packets belonging to an already-established transparent
# proxy connection have a socket with --transparent set. We mark them
# and ACCEPT immediately — no need to walk the full chain again.
-A ${CHAIN_DIVERT} -j MARK --set-mark ${FWMARK}
-A ${CHAIN_DIVERT} -j ACCEPT

# === BYPASS chain (reserved IPv4 addresses) ===
# Traffic to private/reserved ranges must never enter the proxy.
# ACCEPT is intentionally terminal for the current table. RETURN here would
# fall back into PRIVSTACK_OUT/PRIVSTACK_PRE and continue to the APP/TPROXY
# rules, so bypassed destinations could still be marked or intercepted.
$(for cidr in ${RESERVED_IPV4}; do
    echo "-A ${CHAIN_BYPASS} -d ${cidr} -j ACCEPT"
done)

# === APP chain (UID whitelist) ===
# In whitelist mode, only explicitly listed UIDs get marked for proxying.
# Default policy: RETURN (direct, no proxy).
$(if [ "${APP_MODE}" = "whitelist" ]; then
    for uid in ${APP_UIDS}; do
        echo "-A ${CHAIN_APP} -m owner --uid-owner ${uid} -j MARK --set-mark ${FWMARK}"
    done
elif [ "${APP_MODE}" = "blacklist" ]; then
    for uid in ${APP_UIDS}; do
        echo "-A ${CHAIN_APP} -m owner --uid-owner ${uid} -j RETURN"
    done
    echo "-A ${CHAIN_APP} -j MARK --set-mark ${FWMARK}"
    # If APP_MODE is "all", mark everything (except what was already returned)
elif [ "${APP_MODE}" = "all" ]; then
    echo "-A ${CHAIN_APP} -j MARK --set-mark ${FWMARK}"
fi)
# Default: RETURN (direct) — packets not matching any UID above go direct.
-A ${CHAIN_APP} -j RETURN

# === OUTPUT chain (mangle) ===
# Dispatches outgoing packets through the filtering pipeline.
# Order matters — early returns prevent unnecessary processing.

# 1. Loop prevention: packets from sing-box (GID ${CORE_GID}) go direct.
#    Without this, sing-box's own outbound traffic would be re-marked and
#    loop back into itself infinitely.
-A ${CHAIN_OUT} -m owner --gid-owner ${CORE_GID} -j RETURN

# 2. Belt-and-suspenders: if a packet is already marked 0xff by the kernel
#    or another subsystem, let it through. This catches edge cases where
#    the mark is set outside our control (e.g. VPN apps, other modules).
-A ${CHAIN_OUT} -m mark --mark 0xff -j RETURN

# 3. ICMP bypass: let ICMP (ping) through directly. TPROXY cannot handle
#    ICMP, and blocking it causes diagnostic black holes.
-A ${CHAIN_OUT} -p icmp -j RETURN

# 4. Local listener protection: tproxy, DNS and API ports are internal
#    control-plane surfaces. Only root and the sing-box core GID may connect
#    directly; app traffic must enter through marking/TPROXY or DNS REDIRECT.
-A ${CHAIN_OUT} -p tcp --dport ${TPROXY_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p udp --dport ${TPROXY_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${DNS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p udp --dport ${DNS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${API_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${HTTP_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${TPROXY_PORT} -j RETURN
-A ${CHAIN_OUT} -p udp --dport ${TPROXY_PORT} -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${DNS_PORT} -j RETURN
-A ${CHAIN_OUT} -p udp --dport ${DNS_PORT} -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${API_PORT} -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${HTTP_PORT} -j RETURN

# 6. Bypass UIDs: specific UIDs that must always go direct.
#    UID 1073 = NetworkStack — needed for captive portal detection.
#    Android checks connectivity by hitting a known URL; if this goes
#    through the proxy, captive portals never trigger.
$(for uid in ${BYPASS_UIDS}; do
    echo "-A ${CHAIN_OUT} -m owner --uid-owner ${uid} -j RETURN"
done)

# 7. Reserved IP bypass: send to BYPASS chain.
-A ${CHAIN_OUT} -j ${CHAIN_BYPASS}

# 8. UID whitelist/all: send to APP chain for mark decision.
-A ${CHAIN_OUT} -j ${CHAIN_APP}

# === PREROUTING chain (mangle) ===
# Handles incoming packets after routing decision. TPROXY happens here
# because it can only work in PREROUTING (not OUTPUT).

# 1. DIVERT optimization: if a socket already has --transparent set,
#    this is an established tproxy connection. Mark and accept immediately.
#    This avoids re-walking the entire chain for every packet in a flow.
-A ${CHAIN_PRE} -p tcp -m socket --transparent -j ${CHAIN_DIVERT}
-A ${CHAIN_PRE} -p udp -m socket --transparent -j ${CHAIN_DIVERT}

# 2. Reserved IP bypass in PREROUTING too. Packets arriving here from
#    other interfaces (e.g. hotspot clients) also need bypass for
#    private ranges.
-A ${CHAIN_PRE} -j ${CHAIN_BYPASS}

# 3. TPROXY: redirect marked TCP packets to sing-box's tproxy port.
#    --tproxy-mark re-applies the mark so policy routing keeps working.
-A ${CHAIN_PRE} -p tcp -m mark --mark ${FWMARK} -j TPROXY --on-ip 127.0.0.1 --on-port ${TPROXY_PORT} --tproxy-mark ${FWMARK}

# 4. TPROXY: same for UDP.
-A ${CHAIN_PRE} -p udp -m mark --mark ${FWMARK} -j TPROXY --on-ip 127.0.0.1 --on-port ${TPROXY_PORT} --tproxy-mark ${FWMARK}

# === Hook into built-in chains ===
# Jump from the kernel's OUTPUT/PREROUTING into our dispatcher chains.
# These are the entry points — everything above is in custom chains.
-A OUTPUT -j ${CHAIN_OUT}
-A PREROUTING -j ${CHAIN_PRE}

COMMIT
MANGLE_V4_EOF
}

# Generate IPv6 mangle table rules — mirrors IPv4 exactly,
# except using IPv6-specific reserved ranges and icmpv6.
gen_mangle_v6() {
    cat <<MANGLE_V6_EOF
*mangle

# --- Create custom chains (flush if they exist) ---
:${CHAIN_OUT} - [0:0]
:${CHAIN_PRE} - [0:0]
:${CHAIN_APP} - [0:0]
:${CHAIN_BYPASS} - [0:0]
:${CHAIN_DIVERT} - [0:0]

# === DIVERT chain ===
-A ${CHAIN_DIVERT} -j MARK --set-mark ${FWMARK}
-A ${CHAIN_DIVERT} -j ACCEPT

# === BYPASS chain (reserved IPv6 addresses) ===
$(for cidr in ${RESERVED_IPV6}; do
    echo "-A ${CHAIN_BYPASS} -d ${cidr} -j ACCEPT"
done)

# === APP chain (UID whitelist) ===
$(if [ "${APP_MODE}" = "whitelist" ]; then
    for uid in ${APP_UIDS}; do
        echo "-A ${CHAIN_APP} -m owner --uid-owner ${uid} -j MARK --set-mark ${FWMARK}"
    done
elif [ "${APP_MODE}" = "blacklist" ]; then
    for uid in ${APP_UIDS}; do
        echo "-A ${CHAIN_APP} -m owner --uid-owner ${uid} -j RETURN"
    done
    echo "-A ${CHAIN_APP} -j MARK --set-mark ${FWMARK}"
elif [ "${APP_MODE}" = "all" ]; then
    echo "-A ${CHAIN_APP} -j MARK --set-mark ${FWMARK}"
fi)
-A ${CHAIN_APP} -j RETURN

# === OUTPUT chain (mangle) ===

# 1. Loop prevention (sing-box GID)
-A ${CHAIN_OUT} -m owner --gid-owner ${CORE_GID} -j RETURN

# 2. Belt-and-suspenders mark check
-A ${CHAIN_OUT} -m mark --mark 0xff -j RETURN

# 3. ICMPv6 bypass — same rationale as ICMP for v4.
#    Also critical for IPv6 NDP (neighbor discovery) which uses ICMPv6.
-A ${CHAIN_OUT} -p icmpv6 -j RETURN

# 4. Local listener protection: root and core-GID only.
-A ${CHAIN_OUT} -p tcp --dport ${TPROXY_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p udp --dport ${TPROXY_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${DNS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p udp --dport ${DNS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${API_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${HTTP_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${TPROXY_PORT} -j RETURN
-A ${CHAIN_OUT} -p udp --dport ${TPROXY_PORT} -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${DNS_PORT} -j RETURN
-A ${CHAIN_OUT} -p udp --dport ${DNS_PORT} -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${API_PORT} -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${HTTP_PORT} -j RETURN

# 6. Bypass UIDs
$(for uid in ${BYPASS_UIDS}; do
    echo "-A ${CHAIN_OUT} -m owner --uid-owner ${uid} -j RETURN"
done)

# 7. Reserved IP bypass
-A ${CHAIN_OUT} -j ${CHAIN_BYPASS}

# 8. UID whitelist/all
-A ${CHAIN_OUT} -j ${CHAIN_APP}

# === PREROUTING chain (mangle) ===

# 1. DIVERT optimization
-A ${CHAIN_PRE} -p tcp -m socket --transparent -j ${CHAIN_DIVERT}
-A ${CHAIN_PRE} -p udp -m socket --transparent -j ${CHAIN_DIVERT}

# 2. Reserved IP bypass
-A ${CHAIN_PRE} -j ${CHAIN_BYPASS}

# 3. TPROXY TCP
-A ${CHAIN_PRE} -p tcp -m mark --mark ${FWMARK} -j TPROXY --on-ip ::1 --on-port ${TPROXY_PORT} --tproxy-mark ${FWMARK}

# 4. TPROXY UDP
-A ${CHAIN_PRE} -p udp -m mark --mark ${FWMARK} -j TPROXY --on-ip ::1 --on-port ${TPROXY_PORT} --tproxy-mark ${FWMARK}

# === Hook into built-in chains ===
-A OUTPUT -j ${CHAIN_OUT}
-A PREROUTING -j ${CHAIN_PRE}

COMMIT
MANGLE_V6_EOF
}

# Generate IPv4 nat table rules for DNS redirection.
# DNS queries (UDP port 53) from selected UIDs get redirected to
# sing-box's DNS listener so we can apply DNS-based routing rules.
gen_nat_v4() {
    cat <<NAT_V4_EOF
*nat

# --- Create DNS redirect chain ---
:${CHAIN_DNS} - [0:0]

# 1. Loop prevention: sing-box must not have its own DNS queries
#    redirected back to itself.
-A ${CHAIN_DNS} -m owner --gid-owner ${CORE_GID} -j RETURN

# 2. Bypass UIDs: these apps must keep their own DNS path too.
$(for uid in ${BYPASS_UIDS}; do
    echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -j RETURN"
done)

# 3. DNS redirect rules — depends on DNS_MODE.
$(if [ "${DNS_MODE}" = "per_uid" ]; then
    # per_uid: only redirect DNS for whitelisted UIDs.
    # This means non-proxied apps keep using the system DNS resolver,
    # which is important for apps that rely on local DNS (e.g. mDNS).
    if [ "${APP_MODE}" = "whitelist" ]; then
        for uid in ${APP_UIDS}; do
            echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
            echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
        done
    elif [ "${APP_MODE}" = "blacklist" ]; then
        for uid in ${APP_UIDS}; do
            echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -j RETURN"
        done
        echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
        echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    elif [ "${APP_MODE}" = "all" ]; then
        # "all" mode with per_uid DNS doesn't make sense — redirect all DNS
        echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
        echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    fi
elif [ "${DNS_MODE}" = "all" ]; then
    # all: redirect every DNS query to sing-box, regardless of UID.
    # This ensures consistent DNS resolution across all apps, but means
    # even non-proxied apps use sing-box's DNS.
    echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
fi)

# === Hook into built-in OUTPUT (nat) ===
-A OUTPUT -j ${CHAIN_DNS}

COMMIT
NAT_V4_EOF
}

# Generate IPv6 nat table rules for DNS — mirrors v4.
gen_nat_v6() {
    cat <<NAT_V6_EOF
*nat

# --- Create DNS redirect chain ---
:${CHAIN_DNS} - [0:0]

# 1. Loop prevention
-A ${CHAIN_DNS} -m owner --gid-owner ${CORE_GID} -j RETURN

# 2. Bypass UIDs
$(for uid in ${BYPASS_UIDS}; do
    echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -j RETURN"
done)

# 3. DNS redirect rules
$(if [ "${DNS_MODE}" = "per_uid" ]; then
    if [ "${APP_MODE}" = "whitelist" ]; then
        for uid in ${APP_UIDS}; do
            echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
            echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
        done
    elif [ "${APP_MODE}" = "blacklist" ]; then
        for uid in ${APP_UIDS}; do
            echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -j RETURN"
        done
        echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
        echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    elif [ "${APP_MODE}" = "all" ]; then
        echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
        echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    fi
elif [ "${DNS_MODE}" = "all" ]; then
    echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
fi)

# === Hook into built-in OUTPUT (nat) ===
-A OUTPUT -j ${CHAIN_DNS}

COMMIT
NAT_V6_EOF
}

# ============================================================================
# Backup existing iptables state (for clean rollback on stop)
# ============================================================================

backup_rules() {
    mkdir -p "${SNAPSHOT_DIR}"

    # Save current state so we can restore on stop.
    # iptables-save includes all tables and chains.
    iptables-save ${IPT_WAIT} > "${SNAPSHOT_DIR}/iptables_backup.rules" 2>/dev/null || true
    ip6tables-save ${IPT_WAIT} > "${SNAPSHOT_DIR}/ip6tables_backup.rules" 2>/dev/null || true

    log_info "Backed up current iptables state"
}

# ============================================================================
# Policy routing — needed for TPROXY to work
#
# Marked packets need to be routed to the local machine (where sing-box
# listens). We add a policy rule: packets with $FWMARK use a dedicated
# routing table that has a single route: send everything to loopback.
# ============================================================================

setup_policy_routing() {
    # Remove stale duplicate rules from interrupted/failed older starts before
    # adding a fresh pair. Android permits duplicate ip rules, and one `del`
    # removes only one entry.
    teardown_policy_routing

    # IPv4 policy route
    # "ip rule": if packet has fwmark $FWMARK, use routing table $ROUTE_TABLE
    ip rule add fwmark ${FWMARK} table ${ROUTE_TABLE} 2>/dev/null || true
    # The routing table sends all traffic to loopback (local delivery)
    ip route add local default dev lo table ${ROUTE_TABLE} 2>/dev/null || true

    # IPv6 policy route — exact mirror
    ip -6 rule add fwmark ${FWMARK} table ${ROUTE_TABLE_V6} 2>/dev/null || true
    ip -6 route add local default dev lo table ${ROUTE_TABLE_V6} 2>/dev/null || true

    log_info "Policy routing configured (table=${ROUTE_TABLE}/${ROUTE_TABLE_V6}, mark=${FWMARK})"
}

teardown_policy_routing() {
    # Remove policy rules and routes. Suppress errors if they don't exist.
    while ip rule del fwmark ${FWMARK} table ${ROUTE_TABLE} 2>/dev/null; do :; done
    ip route del local default dev lo table ${ROUTE_TABLE} 2>/dev/null || true
    ip route flush table ${ROUTE_TABLE} 2>/dev/null || true

    while ip -6 rule del fwmark ${FWMARK} table ${ROUTE_TABLE_V6} 2>/dev/null; do :; done
    ip -6 route del local default dev lo table ${ROUTE_TABLE_V6} 2>/dev/null || true
    ip -6 route flush table ${ROUTE_TABLE_V6} 2>/dev/null || true

    log_info "Policy routing removed"
}

# ============================================================================
# Chain cleanup — remove our chains from the system
#
# We must:
# 1. Remove jumps from built-in chains to our chains
# 2. Flush our custom chains
# 3. Delete our custom chains
#
# This is used when iptables-restore of the backup fails, or as a
# fallback cleanup method.
# ============================================================================

flush_chains() {
    local ipt="$1"  # "iptables" or "ip6tables"

    log_info "Flushing ${ipt} chains..."

    # --- mangle table ---
    # Remove jumps from OUTPUT and PREROUTING to our chains.
    # Loop because there might be multiple entries (idempotency).
    while ${ipt} ${IPT_WAIT} -t mangle -D OUTPUT -j ${CHAIN_OUT} 2>/dev/null; do :; done
    while ${ipt} ${IPT_WAIT} -t mangle -D PREROUTING -j ${CHAIN_PRE} 2>/dev/null; do :; done

    # Flush and delete our custom chains in mangle
    for chain in ${CHAIN_OUT} ${CHAIN_PRE} ${CHAIN_APP} ${CHAIN_BYPASS} ${CHAIN_DIVERT}; do
        ${ipt} ${IPT_WAIT} -t mangle -F ${chain} 2>/dev/null || true
        ${ipt} ${IPT_WAIT} -t mangle -X ${chain} 2>/dev/null || true
    done

    # --- nat table ---
    while ${ipt} ${IPT_WAIT} -t nat -D OUTPUT -j ${CHAIN_DNS} 2>/dev/null; do :; done

    ${ipt} ${IPT_WAIT} -t nat -F ${CHAIN_DNS} 2>/dev/null || true
    ${ipt} ${IPT_WAIT} -t nat -X ${CHAIN_DNS} 2>/dev/null || true

    log_info "${ipt} chains flushed and removed"
}

flush_nat_chain() {
    local ipt="$1"  # "iptables" or "ip6tables"

    while ${ipt} ${IPT_WAIT} -t nat -D OUTPUT -j ${CHAIN_DNS} 2>/dev/null; do :; done
    ${ipt} ${IPT_WAIT} -t nat -F ${CHAIN_DNS} 2>/dev/null || true
    ${ipt} ${IPT_WAIT} -t nat -X ${CHAIN_DNS} 2>/dev/null || true
}

# ============================================================================
# START — apply all rules
# ============================================================================

do_start() {
    log_info "========================================="
    log_info "Starting PrivStack iptables ${SCRIPT_VERSION} (mode=${APP_MODE}, proxy=${PROXY_MODE})"
    log_info "  TPROXY_PORT=${TPROXY_PORT}"
    log_info "  DNS_PORT=${DNS_PORT}"
    log_info "  API_PORT=${API_PORT}"
    log_info "  HTTP_PORT=${HTTP_PORT}"
    log_info "  FWMARK=${FWMARK}"
    log_info "  CORE_GID=${CORE_GID}"
    log_info "  APP_UIDS=${APP_UIDS:-<none>}"
    log_info "  BYPASS_UIDS=${BYPASS_UIDS:-<none>}"
    log_info "  DNS_MODE=${DNS_MODE}"
    log_info "========================================="

    # ---- Step 1: Clean any leftover rules from a previous run ----
    # This ensures idempotency — running start twice doesn't create
    # duplicate chains or rules.
    log_info "Cleaning up any leftover rules..."
    flush_chains iptables
    flush_chains ip6tables

    # ---- Step 2: Backup current iptables state ----
    backup_rules

    # ---- Step 3: Save runtime config snapshot ----
    save_snapshot

    # ---- Step 4: Setup policy routing ----
    setup_policy_routing

    # ---- Step 5: Generate rules files ----
    mkdir -p "${SNAPSHOT_DIR}"

    # iptables.sh owns mangle + policy routing. DNS nat is owned by dns.sh.
    gen_mangle_v4 > "${SNAPSHOT_DIR}/iptables.rules"

    gen_mangle_v6 > "${SNAPSHOT_DIR}/ip6tables.rules"

    log_info "Rules files generated"

    # ---- Step 6: Apply rules atomically via iptables-restore ----
    # --noflush: don't flush existing rules in tables we're not touching.
    # This is critical — we only want to add our chains, not destroy
    # other modules' rules (e.g. afwall, adaway).

    log_info "Applying IPv4 rules..."
    if ! iptables-restore ${IPT_WAIT} --noflush < "${SNAPSHOT_DIR}/iptables.rules"; then
        log_error "iptables-restore failed for IPv4!"
        log_error "Rules file content:"
        cat "${SNAPSHOT_DIR}/iptables.rules" >&2
        do_stop
        exit 1
    fi

    log_info "Applying IPv6 rules..."
    if ! ip6tables-restore ${IPT_WAIT} --noflush < "${SNAPSHOT_DIR}/ip6tables.rules"; then
        log_error "ip6tables-restore failed for IPv6!"
        log_error "Rules file content:"
        cat "${SNAPSHOT_DIR}/ip6tables.rules" >&2
        # Roll back v4 too
        do_stop
        exit 1
    fi

    # ---- Step 7: Verify critical chains exist ----
    if ! iptables ${IPT_WAIT} -t mangle -L ${CHAIN_OUT} -n >/dev/null 2>&1; then
        log_error "Verification failed: ${CHAIN_OUT} not found in mangle table!"
        do_stop
        exit 1
    fi

    if ! ip6tables ${IPT_WAIT} -t mangle -L ${CHAIN_OUT} -n >/dev/null 2>&1; then
        log_error "Verification failed: ${CHAIN_OUT} not found in ip6tables mangle table!"
        do_stop
        exit 1
    fi

    log_info "========================================="
    log_info "PrivStack iptables rules applied successfully"
    log_info "========================================="
}

# ============================================================================
# STOP — remove all rules and restore clean state
# ============================================================================

do_stop() {
    log_info "Stopping PrivStack iptables..."

    # Try to load snapshot for accurate teardown values.
    # If snapshot doesn't exist, use current env (best effort).
    if [ -f "${SNAPSHOT_DIR}/env.sh" ]; then
        log_info "Loading runtime snapshot for teardown..."
        load_snapshot
    else
        log_warn "No runtime snapshot found — using current environment"
    fi

    # ---- Step 1: Remove iptables rules ----
    # Strategy: try to restore backup first (atomic). If that fails,
    # fall back to manual chain flush (safe but slower).

    local v4_restored=0
    local v6_restored=0

    if [ -f "${SNAPSHOT_DIR}/iptables_backup.rules" ]; then
        log_info "Restoring IPv4 iptables from backup..."
        if iptables-restore ${IPT_WAIT} < "${SNAPSHOT_DIR}/iptables_backup.rules" 2>/dev/null; then
            v4_restored=1
            log_info "IPv4 iptables restored from backup"
        else
            log_warn "IPv4 backup restore failed — falling back to manual flush"
        fi
    fi

    if [ -f "${SNAPSHOT_DIR}/ip6tables_backup.rules" ]; then
        log_info "Restoring IPv6 ip6tables from backup..."
        if ip6tables-restore ${IPT_WAIT} < "${SNAPSHOT_DIR}/ip6tables_backup.rules" 2>/dev/null; then
            v6_restored=1
            log_info "IPv6 ip6tables restored from backup"
        else
            log_warn "IPv6 backup restore failed — falling back to manual flush"
        fi
    fi

    # Fallback: manual chain removal if restore didn't work
    if [ "${v4_restored}" -eq 0 ]; then
        flush_chains iptables
    fi

    if [ "${v6_restored}" -eq 0 ]; then
        flush_chains ip6tables
    fi

    # ---- Step 2: Remove policy routing ----
    teardown_policy_routing

    # ---- Step 3: Clean up snapshot files ----
    if [ -d "${SNAPSHOT_DIR}" ]; then
        rm -f "${SNAPSHOT_DIR}/iptables.rules"
        rm -f "${SNAPSHOT_DIR}/ip6tables.rules"
        rm -f "${SNAPSHOT_DIR}/iptables_backup.rules"
        rm -f "${SNAPSHOT_DIR}/ip6tables_backup.rules"
        rm -f "${SNAPSHOT_DIR}/env.sh"
        log_info "Snapshot files cleaned up"
    fi

    log_info "PrivStack iptables rules removed"
}

# ============================================================================
# MAIN — dispatch based on command
# ============================================================================

case "${1:-}" in
    start)
        validate_env
        do_start
        ;;
    stop)
        # For stop, we try snapshot first, then env vars.
        # Allow stop to work even with partial env.
        SNAPSHOT_DIR="${PRIVSTACK_DIR:-/data/adb/privstack}/run"
        if [ -f "${SNAPSHOT_DIR}/env.sh" ]; then
            load_snapshot
        fi
        # Set defaults for any vars still missing (stop should never fail)
        FWMARK="${FWMARK:-0x2023}"
        ROUTE_TABLE="${ROUTE_TABLE:-2023}"
        ROUTE_TABLE_V6="${ROUTE_TABLE_V6:-2024}"
        do_stop
        ;;
    *)
        echo "Usage: $0 {start|stop}"
        echo ""
        echo "Environment variables must be set by the caller (privd)."
        echo "See script header for the full list."
        exit 1
        ;;
esac

exit 0
