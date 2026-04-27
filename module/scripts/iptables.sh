#!/system/bin/sh
# ============================================================================
# PrivStack — iptables.sh
# Whitelist-mode transparent proxy via TPROXY + iptables.
# Main orchestration only: env/snapshot/apply/teardown/status.
# Rule rendering and listener verification live in scripts/lib/.
# POSIX sh compatible. No bashisms.
# ============================================================================

set -eu

TAG="PrivStack:iptables"
SCRIPT_VERSION="v1.8.0"
SCRIPT_DIR="${0%/*}"

if [ -f "${SCRIPT_DIR}/lib/privstack_env.sh" ]; then
    . "${SCRIPT_DIR}/lib/privstack_env.sh"
fi
if [ -f "${SCRIPT_DIR}/lib/privstack_netstack.sh" ]; then
    . "${SCRIPT_DIR}/lib/privstack_netstack.sh"
fi
if [ -f "${SCRIPT_DIR}/lib/privstack_iptables_rules.sh" ]; then
    . "${SCRIPT_DIR}/lib/privstack_iptables_rules.sh"
fi

CHAIN_PREFIX="PRIVSTACK"
CHAIN_OUT="${CHAIN_PREFIX}_OUT"
CHAIN_PRE="${CHAIN_PREFIX}_PRE"
CHAIN_APP="${CHAIN_PREFIX}_APP"
CHAIN_BYPASS="${CHAIN_PREFIX}_BYPASS"
CHAIN_DNS="${CHAIN_PREFIX}_DNS"
CHAIN_DIVERT="${CHAIN_PREFIX}_DIVERT"

SNAPSHOT_DIR="${RUN_DIR:-${PRIVSTACK_DIR:-/data/adb/privstack}/run}"
IPT_WAIT="${IPT_WAIT:--w 100}"

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

RESERVED_IPV6="
::1/128
fe80::/10
fc00::/7
ff00::/8
::ffff:0:0/96
"

log_info() {
    echo "[${TAG}] INFO: $*"
}

log_warn() {
    echo "[${TAG}] WARN: $*" >&2
}

log_error() {
    echo "[${TAG}] ERROR: $*" >&2
}

require_rule_renderer() {
    if ! command -v gen_mangle_v4 >/dev/null 2>&1 || ! command -v check_local_listener_protection >/dev/null 2>&1; then
        log_error "missing scripts/lib/privstack_iptables_rules.sh"
        exit 1
    fi
}

validate_env() {
    missing=""

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

    PROXY_MODE="${PROXY_MODE:-tproxy}"
    PROXY_UIDS="${PROXY_UIDS:-}"
    DIRECT_UIDS="${DIRECT_UIDS:-}"
    BYPASS_UIDS="${BYPASS_UIDS:-}"
    SOCKS_PORT="${SOCKS_PORT:-0}"
    HTTP_PORT="${HTTP_PORT:-0}"
    CHAIN_PROXY_PORTS="${CHAIN_PROXY_PORTS:-}"
    CHAIN_PROXY_UIDS="${CHAIN_PROXY_UIDS:-}"
    DNS_MODE="${DNS_MODE:-per_uid}"
    DNS_SCOPE="${DNS_SCOPE:-}"

    if [ -z "$DNS_SCOPE" ]; then
        case "$DNS_MODE:$APP_MODE" in
            off:*) DNS_SCOPE="off" ;;
            all:*) DNS_SCOPE="all" ;;
            per_uid:blacklist|uid:blacklist) DNS_SCOPE="all_except_uids" ;;
            per_uid:*|uid:*) DNS_SCOPE="uids" ;;
            *) DNS_SCOPE="off" ;;
        esac
    fi
}

ipv6_mangle_available() {
    command -v ip6tables >/dev/null 2>&1 &&
        ip6tables ${IPT_WAIT} -t mangle -L >/dev/null 2>&1 &&
        command -v ip6tables-restore >/dev/null 2>&1
}

ip_rule_present() {
    _family="$1"
    _table="$2"
    if [ "$_family" = "6" ]; then
        _cmd="ip -6 rule show"
    else
        _cmd="ip rule show"
    fi
    $_cmd 2>/dev/null | awk -v mark="$(printf '%s' "$FWMARK" | tr 'A-F' 'a-f')" -v table="$_table" '
        {
            line=tolower($0)
            n=split(line, f, /[[:space:]]+/)
            mark_ok=0
            table_ok=0
            for (i=1; i<n; i++) {
                if (f[i] == "fwmark" && mark != "" && (f[i+1] == mark || index(f[i+1], mark "/") == 1)) { mark_ok=1 }
                if ((f[i] == "lookup" || f[i] == "table") && table != "" && f[i+1] == table) { table_ok=1 }
            }
            if (mark_ok && table_ok) { found=1; exit }
        }
        END { exit found ? 0 : 1 }'
}

save_snapshot() {
    mkdir -p "${SNAPSHOT_DIR}"

    cat > "${SNAPSHOT_DIR}/env.sh" <<SNAPSHOT_EOF
# PrivStack runtime snapshot — generated at $(date)
TPROXY_PORT=${TPROXY_PORT}
DNS_PORT=${DNS_PORT}
API_PORT=${API_PORT}
SOCKS_PORT=${SOCKS_PORT}
HTTP_PORT=${HTTP_PORT}
CHAIN_PROXY_PORTS="${CHAIN_PROXY_PORTS}"
CHAIN_PROXY_UIDS="${CHAIN_PROXY_UIDS}"
FWMARK=${FWMARK}
ROUTE_TABLE=${ROUTE_TABLE}
ROUTE_TABLE_V6=${ROUTE_TABLE_V6}
CORE_GID=${CORE_GID}
APP_MODE=${APP_MODE}
PROXY_UIDS="${PROXY_UIDS}"
DIRECT_UIDS="${DIRECT_UIDS}"
BYPASS_UIDS="${BYPASS_UIDS}"
DNS_SCOPE=${DNS_SCOPE}
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

setup_policy_routing() {
    teardown_policy_routing

    ip rule add fwmark ${FWMARK} table ${ROUTE_TABLE} 2>/dev/null || true
    ip route add local default dev lo table ${ROUTE_TABLE} 2>/dev/null || true
    ip -6 rule add fwmark ${FWMARK} table ${ROUTE_TABLE_V6} 2>/dev/null || true
    ip -6 route add local default dev lo table ${ROUTE_TABLE_V6} 2>/dev/null || true

    log_info "Policy routing configured (table=${ROUTE_TABLE}/${ROUTE_TABLE_V6}, mark=${FWMARK})"
}

teardown_policy_routing() {
    privstack_delete_policy_routes
    log_info "Policy routing removed"
}

flush_chains() {
    ipt="$1"

    log_info "Flushing ${ipt} chains..."

    while ${ipt} ${IPT_WAIT} -t mangle -D OUTPUT -j ${CHAIN_OUT} 2>/dev/null; do :; done
    while ${ipt} ${IPT_WAIT} -t mangle -D PREROUTING -j ${CHAIN_PRE} 2>/dev/null; do :; done

    for chain in ${CHAIN_OUT} ${CHAIN_PRE} ${CHAIN_APP} ${CHAIN_BYPASS} ${CHAIN_DIVERT}; do
        ${ipt} ${IPT_WAIT} -t mangle -F ${chain} 2>/dev/null || true
        ${ipt} ${IPT_WAIT} -t mangle -X ${chain} 2>/dev/null || true
    done

    while ${ipt} ${IPT_WAIT} -t nat -D OUTPUT -j ${CHAIN_DNS} 2>/dev/null; do :; done
    ${ipt} ${IPT_WAIT} -t nat -F ${CHAIN_DNS} 2>/dev/null || true
    ${ipt} ${IPT_WAIT} -t nat -X ${CHAIN_DNS} 2>/dev/null || true

    log_info "${ipt} chains flushed and removed"
}

do_start() {
    log_info "========================================="
    log_info "Starting PrivStack iptables ${SCRIPT_VERSION} (mode=${APP_MODE}, proxy=${PROXY_MODE})"
    log_info "  TPROXY_PORT=${TPROXY_PORT}"
    log_info "  DNS_PORT=${DNS_PORT}"
    log_info "  API_PORT=${API_PORT}"
    log_info "  SOCKS_PORT=${SOCKS_PORT}"
    log_info "  HTTP_PORT=${HTTP_PORT}"
    log_info "  CHAIN_PROXY_PORTS=${CHAIN_PROXY_PORTS:-<none>}"
    log_info "  CHAIN_PROXY_UIDS=${CHAIN_PROXY_UIDS:-<none>}"
    log_info "  FWMARK=${FWMARK}"
    log_info "  CORE_GID=${CORE_GID}"
    log_info "  PROXY_UIDS=${PROXY_UIDS:-<none>}"
    log_info "  DIRECT_UIDS=${DIRECT_UIDS:-<none>}"
    log_info "  BYPASS_UIDS=${BYPASS_UIDS:-<none>}"
    log_info "  DNS_SCOPE=${DNS_SCOPE}"
    log_info "========================================="

    flush_chains iptables
    if ipv6_mangle_available; then
        flush_chains ip6tables
    else
        log_warn "IPv6 iptables mangle/restore unavailable; continuing with IPv4-only interception"
    fi
    save_snapshot
    setup_policy_routing

    mkdir -p "${SNAPSHOT_DIR}"
    gen_mangle_v4 > "${SNAPSHOT_DIR}/iptables.rules"
    gen_mangle_v6 > "${SNAPSHOT_DIR}/ip6tables.rules"
    log_info "Rules files generated"

    log_info "Applying IPv4 rules..."
    if ! iptables-restore ${IPT_WAIT} --noflush < "${SNAPSHOT_DIR}/iptables.rules"; then
        log_error "iptables-restore failed for IPv4!"
        cat "${SNAPSHOT_DIR}/iptables.rules" >&2
        do_stop
        exit 1
    fi

    if ipv6_mangle_available; then
        log_info "Applying IPv6 rules..."
        if ! ip6tables-restore ${IPT_WAIT} --noflush < "${SNAPSHOT_DIR}/ip6tables.rules"; then
            log_warn "ip6tables-restore failed for IPv6; continuing with IPv4-only interception"
            cat "${SNAPSHOT_DIR}/ip6tables.rules" >&2
            flush_chains ip6tables
        fi
    else
        log_warn "Skipping IPv6 rules apply: ip6tables/ip6tables-restore unavailable"
    fi

    if ! iptables ${IPT_WAIT} -t mangle -L ${CHAIN_OUT} -n >/dev/null 2>&1; then
        log_error "Verification failed: ${CHAIN_OUT} not found in mangle table!"
        do_stop
        exit 1
    fi
    if ipv6_mangle_available; then
        if ! ip6tables ${IPT_WAIT} -t mangle -L ${CHAIN_OUT} -n >/dev/null 2>&1; then
            log_warn "Verification degraded: ${CHAIN_OUT} not found in ip6tables mangle table"
        fi
    fi

    log_info "========================================="
    log_info "PrivStack iptables rules applied successfully"
    log_info "========================================="
}

do_stop() {
    log_info "Stopping PrivStack iptables..."

    if [ -f "${SNAPSHOT_DIR}/env.sh" ]; then
        log_info "Loading runtime snapshot for teardown..."
        load_snapshot
    else
        log_warn "No runtime snapshot found — using current environment"
    fi

    flush_chains iptables
    if command -v ip6tables >/dev/null 2>&1; then
        flush_chains ip6tables
    fi
    teardown_policy_routing

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

do_status() {
    missing=0

    if ! iptables ${IPT_WAIT} -t mangle -L "${CHAIN_OUT}" -n >/dev/null 2>&1; then
        log_error "missing IPv4 mangle chain ${CHAIN_OUT}"
        missing=1
    fi
    if ! iptables ${IPT_WAIT} -t mangle -L "${CHAIN_PRE}" -n >/dev/null 2>&1; then
        log_error "missing IPv4 mangle chain ${CHAIN_PRE}"
        missing=1
    fi
    if ! iptables ${IPT_WAIT} -t mangle -C OUTPUT -j "${CHAIN_OUT}" >/dev/null 2>&1; then
        log_error "missing IPv4 OUTPUT hook for ${CHAIN_OUT}"
        missing=1
    fi
    if ! iptables ${IPT_WAIT} -t mangle -C PREROUTING -j "${CHAIN_PRE}" >/dev/null 2>&1; then
        log_error "missing IPv4 PREROUTING hook for ${CHAIN_PRE}"
        missing=1
    fi
    if ! check_local_listener_protection iptables; then
        missing=1
    fi

    if ip6tables ${IPT_WAIT} -t mangle -L >/dev/null 2>&1; then
        if ! ip6tables ${IPT_WAIT} -t mangle -L "${CHAIN_OUT}" -n >/dev/null 2>&1; then
            log_error "missing IPv6 mangle chain ${CHAIN_OUT}"
            missing=1
        fi
        if ! ip6tables ${IPT_WAIT} -t mangle -L "${CHAIN_PRE}" -n >/dev/null 2>&1; then
            log_error "missing IPv6 mangle chain ${CHAIN_PRE}"
            missing=1
        fi
        if ! ip6tables ${IPT_WAIT} -t mangle -C OUTPUT -j "${CHAIN_OUT}" >/dev/null 2>&1; then
            log_error "missing IPv6 OUTPUT hook for ${CHAIN_OUT}"
            missing=1
        fi
        if ! ip6tables ${IPT_WAIT} -t mangle -C PREROUTING -j "${CHAIN_PRE}" >/dev/null 2>&1; then
            log_error "missing IPv6 PREROUTING hook for ${CHAIN_PRE}"
            missing=1
        fi
        if ! check_local_listener_protection ip6tables; then
            missing=1
        fi
    fi

    if ! ip_rule_present 4 "$ROUTE_TABLE"; then
        log_error "missing IPv4 policy rule fwmark ${FWMARK} lookup ${ROUTE_TABLE}"
        missing=1
    fi
    if ! ip route show table "${ROUTE_TABLE}" | grep -q "local default dev lo"; then
        log_error "missing IPv4 local route in table ${ROUTE_TABLE}"
        missing=1
    fi
    if ip -6 rule show >/dev/null 2>&1; then
        if ! ip_rule_present 6 "$ROUTE_TABLE_V6"; then
            log_error "missing IPv6 policy rule fwmark ${FWMARK} lookup ${ROUTE_TABLE_V6}"
            missing=1
        fi
        if ! ip -6 route show table "${ROUTE_TABLE_V6}" | grep -q "local default dev lo"; then
            log_error "missing IPv6 local route in table ${ROUTE_TABLE_V6}"
            missing=1
        fi
    fi

    if [ "$missing" -ne 0 ]; then
        exit 1
    fi
    log_info "PrivStack iptables hooks are present"
}

case "${1:-}" in
    start)
        require_rule_renderer
        validate_env
        do_start
        ;;
    stop)
        SNAPSHOT_DIR="${RUN_DIR:-${PRIVSTACK_DIR:-/data/adb/privstack}/run}"
        if [ -f "${SNAPSHOT_DIR}/env.sh" ]; then
            load_snapshot
        fi
        FWMARK="${FWMARK:-0x2023}"
        ROUTE_TABLE="${ROUTE_TABLE:-2023}"
        ROUTE_TABLE_V6="${ROUTE_TABLE_V6:-2024}"
        do_stop
        ;;
    status)
        require_rule_renderer
        validate_env
        do_status
        ;;
    *)
        echo "Usage: $0 {start|stop|status}"
        echo ""
        echo "Environment variables must be set by the caller (privd)."
        exit 1
        ;;
esac

exit 0
