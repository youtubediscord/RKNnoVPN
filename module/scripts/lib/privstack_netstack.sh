#!/system/bin/sh
# Shared PrivStack netstack cleanup helpers.
# POSIX sh compatible; safe for rescue, uninstall, and iptables teardown paths.

if [ -z "${PRIVSTACK_DIR:-}" ]; then
    PRIVSTACK_DIR="/data/adb/privstack"
fi

FWMARK="${FWMARK:-0x2023}"
ROUTE_TABLE="${ROUTE_TABLE:-2023}"
ROUTE_TABLE_V6="${ROUTE_TABLE_V6:-2024}"
IPT_WAIT="${IPT_WAIT:--w 100}"

privstack_netstack_log() {
    if command -v privstack_log_info >/dev/null 2>&1; then
        privstack_log_info "$@"
    else
        echo "[privstack:netstack] INFO: $*"
    fi
}

privstack_cleanup_iptables_table_family() {
    _ipt="$1"
    command -v "$_ipt" >/dev/null 2>&1 || return 0

    for _table in raw mangle nat filter; do
        $_ipt $IPT_WAIT -t "$_table" -S >/dev/null 2>&1 || continue
        privstack_netstack_log "cleaning $_ipt table $_table"

        _n=0
        while [ "$_n" -lt 200 ]; do
            _rule="$($_ipt $IPT_WAIT -t "$_table" -S 2>/dev/null | grep -E -- ' (-j|-g) PRIVSTACK' | head -n 1)"
            [ -z "$_rule" ] && break
            _del="$(printf '%s\n' "$_rule" | sed 's/^-A /-D /')"
            $_ipt $IPT_WAIT -t "$_table" $_del 2>/dev/null || break
            _n=$((_n + 1))
        done

        _n=0
        while [ "$_n" -lt 200 ]; do
            _chain="$($_ipt $IPT_WAIT -t "$_table" -S 2>/dev/null | awk '/^-N PRIVSTACK/ {print $2; exit}')"
            [ -z "$_chain" ] && break
            $_ipt $IPT_WAIT -t "$_table" -F "$_chain" 2>/dev/null || true
            $_ipt $IPT_WAIT -t "$_table" -X "$_chain" 2>/dev/null || true
            _n=$((_n + 1))
        done
    done
}

privstack_cleanup_iptables_all() {
    privstack_cleanup_iptables_table_family iptables
    privstack_cleanup_iptables_table_family ip6tables
    privstack_cleanup_iptables_table_family iptables-legacy
    privstack_cleanup_iptables_table_family ip6tables-legacy
    privstack_cleanup_iptables_table_family iptables-nft
    privstack_cleanup_iptables_table_family ip6tables-nft
}

privstack_delete_policy_routes() {
    _i=0
    while [ "$_i" -lt 100 ]; do
        ip rule del fwmark "$FWMARK" table "$ROUTE_TABLE" 2>/dev/null && { _i=$((_i + 1)); continue; }
        ip rule del fwmark "$FWMARK" 2>/dev/null && { _i=$((_i + 1)); continue; }
        ip rule del table "$ROUTE_TABLE" 2>/dev/null && { _i=$((_i + 1)); continue; }
        break
    done

    _i=0
    while [ "$_i" -lt 100 ]; do
        ip -6 rule del fwmark "$FWMARK" table "$ROUTE_TABLE_V6" 2>/dev/null && { _i=$((_i + 1)); continue; }
        ip -6 rule del fwmark "$FWMARK" 2>/dev/null && { _i=$((_i + 1)); continue; }
        ip -6 rule del table "$ROUTE_TABLE_V6" 2>/dev/null && { _i=$((_i + 1)); continue; }
        break
    done

    ip route del local default dev lo table "$ROUTE_TABLE" 2>/dev/null || true
    ip route flush table "$ROUTE_TABLE" 2>/dev/null || true
    ip -6 route del local default dev lo table "$ROUTE_TABLE_V6" 2>/dev/null || true
    ip -6 route flush table "$ROUTE_TABLE_V6" 2>/dev/null || true
}

privstack_collect_netstack_leftovers() {
    _leftovers=""
    for _ipt in iptables ip6tables iptables-legacy ip6tables-legacy iptables-nft ip6tables-nft; do
        command -v "$_ipt" >/dev/null 2>&1 || continue
        for _table in raw mangle nat filter; do
            _out="$($_ipt $IPT_WAIT -t "$_table" -S 2>/dev/null | grep PRIVSTACK | head -n 1)"
            [ -n "$_out" ] && _leftovers="${_leftovers}${_leftovers:+; }${_ipt}/${_table}: ${_out}"
        done
    done

    _out="$(ip rule show 2>/dev/null | grep -E "$ROUTE_TABLE|$FWMARK" | head -n 1)"
    [ -n "$_out" ] && _leftovers="${_leftovers}${_leftovers:+; }ip rule: ${_out}"
    _out="$(ip -6 rule show 2>/dev/null | grep -E "$ROUTE_TABLE_V6|$FWMARK" | head -n 1)"
    [ -n "$_out" ] && _leftovers="${_leftovers}${_leftovers:+; }ip -6 rule: ${_out}"
    _out="$(ip route show table "$ROUTE_TABLE" 2>/dev/null | head -n 1)"
    [ -n "$_out" ] && _leftovers="${_leftovers}${_leftovers:+; }ip route table ${ROUTE_TABLE}: ${_out}"
    _out="$(ip -6 route show table "$ROUTE_TABLE_V6" 2>/dev/null | head -n 1)"
    [ -n "$_out" ] && _leftovers="${_leftovers}${_leftovers:+; }ip -6 route table ${ROUTE_TABLE_V6}: ${_out}"

    printf '%s\n' "$_leftovers"
}
