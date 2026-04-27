#!/system/bin/sh
# RKNnoVPN iptables rule rendering and listener-protection verification.
# Sourced by scripts/iptables.sh after environment validation/defaulting.

http_port_enabled() {
    [ "${HTTP_PORT:-0}" -gt 0 ] 2>/dev/null
}

socks_port_enabled() {
    [ "${SOCKS_PORT:-0}" -gt 0 ] 2>/dev/null
}

api_port_enabled() {
    [ "${API_PORT:-0}" -gt 0 ] 2>/dev/null
}

emit_api_port_protection() {
    ps_emit_chain="$1"
    if api_port_enabled; then
        echo "-A ${ps_emit_chain} -p tcp --dport ${API_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP"
        echo "-A ${ps_emit_chain} -p tcp --dport ${API_PORT} -j RETURN"
    fi
}

emit_http_port_protection() {
    ps_emit_chain="$1"
    if http_port_enabled; then
        echo "-A ${ps_emit_chain} -p tcp --dport ${HTTP_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP"
        echo "-A ${ps_emit_chain} -p tcp --dport ${HTTP_PORT} -j RETURN"
    fi
}

emit_socks_port_protection() {
    ps_emit_chain="$1"
    if socks_port_enabled; then
        echo "-A ${ps_emit_chain} -p tcp --dport ${SOCKS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP"
        echo "-A ${ps_emit_chain} -p tcp --dport ${SOCKS_PORT} -j RETURN"
    fi
}

chain_proxy_port_reserved() {
    ps_reserved_port="$1"
    ps_reserved_value=""
    for ps_reserved_value in "${TPROXY_PORT}" "${DNS_PORT}" "${API_PORT}" "${SOCKS_PORT}" "${HTTP_PORT}"; do
        if [ -n "${ps_reserved_value}" ] && [ "${ps_reserved_value}" -gt 0 ] 2>/dev/null && [ "${ps_reserved_port}" = "${ps_reserved_value}" ]; then
            return 0
        fi
    done
    return 1
}

emit_chain_proxy_port_protection() {
    ps_emit_chain="$1"
    ps_proxy_port=""
    ps_proxy_uid=""
    if [ -z "${CHAIN_PROXY_PORTS}" ] || [ -z "${CHAIN_PROXY_UIDS}" ]; then
        return 0
    fi
    for ps_proxy_port in ${CHAIN_PROXY_PORTS}; do
        case "${ps_proxy_port}" in
            ''|*[!0-9]*) continue ;;
        esac
        if [ "${ps_proxy_port}" -le 0 ] 2>/dev/null || chain_proxy_port_reserved "${ps_proxy_port}"; then
            continue
        fi
        for ps_proxy_uid in ${CHAIN_PROXY_UIDS}; do
            case "${ps_proxy_uid}" in
                ''|*[!0-9]*) continue ;;
            esac
            echo "-A ${ps_emit_chain} -p tcp --dport ${ps_proxy_port} -m owner --uid-owner ${ps_proxy_uid} -j RETURN"
        done
        echo "-A ${ps_emit_chain} -p tcp --dport ${ps_proxy_port} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP"
        echo "-A ${ps_emit_chain} -p tcp --dport ${ps_proxy_port} -j RETURN"
    done
}

check_listener_protection() {
    ps_check_ipt="$1"
    ps_check_proto="$2"
    ps_check_port="$3"
    ps_check_label="$4"

    if ! ${ps_check_ipt} ${IPT_WAIT} -t mangle -C "${CHAIN_OUT}" -p "${ps_check_proto}" --dport "${ps_check_port}" -m owner ! --uid-owner 0 ! --gid-owner "${CORE_GID}" -j DROP >/dev/null 2>&1; then
        log_error "missing ${ps_check_ipt} ${ps_check_label} ${ps_check_proto}/${ps_check_port} local listener protection"
        return 1
    fi
    return 0
}

check_local_listener_protection() {
    ps_check_ipt="$1"
    ps_listener_missing=0

    check_listener_protection "${ps_check_ipt}" tcp "${TPROXY_PORT}" "TPROXY" || ps_listener_missing=1
    check_listener_protection "${ps_check_ipt}" udp "${TPROXY_PORT}" "TPROXY" || ps_listener_missing=1
    check_listener_protection "${ps_check_ipt}" tcp "${DNS_PORT}" "DNS" || ps_listener_missing=1
    check_listener_protection "${ps_check_ipt}" udp "${DNS_PORT}" "DNS" || ps_listener_missing=1
    if api_port_enabled; then
        check_listener_protection "${ps_check_ipt}" tcp "${API_PORT}" "API" || ps_listener_missing=1
    fi
    if socks_port_enabled; then
        check_listener_protection "${ps_check_ipt}" tcp "${SOCKS_PORT}" "SOCKS" || ps_listener_missing=1
    fi
    if http_port_enabled; then
        check_listener_protection "${ps_check_ipt}" tcp "${HTTP_PORT}" "HTTP" || ps_listener_missing=1
    fi
    if [ -n "${CHAIN_PROXY_UIDS}" ]; then
        for ps_proxy_port in ${CHAIN_PROXY_PORTS}; do
            case "${ps_proxy_port}" in
                ''|*[!0-9]*) continue ;;
            esac
            if [ "${ps_proxy_port}" -le 0 ] 2>/dev/null || chain_proxy_port_reserved "${ps_proxy_port}"; then
                continue
            fi
            check_listener_protection "${ps_check_ipt}" tcp "${ps_proxy_port}" "CHAIN_PROXY" || ps_listener_missing=1
        done
    fi
    return "${ps_listener_missing}"
}

gen_mangle_v4() {
    cat <<MANGLE_V4_EOF
*mangle

:${CHAIN_OUT} - [0:0]
:${CHAIN_PRE} - [0:0]
:${CHAIN_APP} - [0:0]
:${CHAIN_BYPASS} - [0:0]
:${CHAIN_DIVERT} - [0:0]

-A ${CHAIN_DIVERT} -j MARK --set-mark ${FWMARK}
-A ${CHAIN_DIVERT} -j ACCEPT

$(for cidr in ${RESERVED_IPV4}; do
    echo "-A ${CHAIN_BYPASS} -d ${cidr} -j ACCEPT"
done)

$(if [ "${APP_MODE}" = "whitelist" ]; then
    for uid in ${PROXY_UIDS}; do
        echo "-A ${CHAIN_APP} -m owner --uid-owner ${uid} -j MARK --set-mark ${FWMARK}"
    done
elif [ "${APP_MODE}" = "blacklist" ]; then
    for uid in ${DIRECT_UIDS}; do
        echo "-A ${CHAIN_APP} -m owner --uid-owner ${uid} -j RETURN"
    done
    echo "-A ${CHAIN_APP} -j MARK --set-mark ${FWMARK}"
elif [ "${APP_MODE}" = "all" ]; then
    echo "-A ${CHAIN_APP} -j MARK --set-mark ${FWMARK}"
fi)
-A ${CHAIN_APP} -j RETURN

-A ${CHAIN_OUT} -m owner --gid-owner ${CORE_GID} -j RETURN
-A ${CHAIN_OUT} -m mark --mark 0xff -j RETURN
-A ${CHAIN_OUT} -p icmp -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${TPROXY_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p udp --dport ${TPROXY_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${DNS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p udp --dport ${DNS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${TPROXY_PORT} -j RETURN
-A ${CHAIN_OUT} -p udp --dport ${TPROXY_PORT} -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${DNS_PORT} -j RETURN
-A ${CHAIN_OUT} -p udp --dport ${DNS_PORT} -j RETURN
$(emit_api_port_protection "${CHAIN_OUT}")
$(emit_socks_port_protection "${CHAIN_OUT}")
$(emit_http_port_protection "${CHAIN_OUT}")
$(emit_chain_proxy_port_protection "${CHAIN_OUT}")

$(for uid in ${BYPASS_UIDS}; do
    echo "-A ${CHAIN_OUT} -m owner --uid-owner ${uid} -j RETURN"
done)

-A ${CHAIN_OUT} -j ${CHAIN_BYPASS}
-A ${CHAIN_OUT} -j ${CHAIN_APP}

-A ${CHAIN_PRE} -p tcp -m socket --transparent -j ${CHAIN_DIVERT}
-A ${CHAIN_PRE} -p udp -m socket --transparent -j ${CHAIN_DIVERT}
-A ${CHAIN_PRE} -j ${CHAIN_BYPASS}
-A ${CHAIN_PRE} -p tcp -m mark --mark ${FWMARK} -j TPROXY --on-ip 127.0.0.1 --on-port ${TPROXY_PORT} --tproxy-mark ${FWMARK}
-A ${CHAIN_PRE} -p udp -m mark --mark ${FWMARK} -j TPROXY --on-ip 127.0.0.1 --on-port ${TPROXY_PORT} --tproxy-mark ${FWMARK}

-A OUTPUT -j ${CHAIN_OUT}
-A PREROUTING -j ${CHAIN_PRE}

COMMIT
MANGLE_V4_EOF
}

gen_mangle_v6() {
    cat <<MANGLE_V6_EOF
*mangle

:${CHAIN_OUT} - [0:0]
:${CHAIN_PRE} - [0:0]
:${CHAIN_APP} - [0:0]
:${CHAIN_BYPASS} - [0:0]
:${CHAIN_DIVERT} - [0:0]

-A ${CHAIN_DIVERT} -j MARK --set-mark ${FWMARK}
-A ${CHAIN_DIVERT} -j ACCEPT

$(for cidr in ${RESERVED_IPV6}; do
    echo "-A ${CHAIN_BYPASS} -d ${cidr} -j ACCEPT"
done)

$(if [ "${APP_MODE}" = "whitelist" ]; then
    for uid in ${PROXY_UIDS}; do
        echo "-A ${CHAIN_APP} -m owner --uid-owner ${uid} -j MARK --set-mark ${FWMARK}"
    done
elif [ "${APP_MODE}" = "blacklist" ]; then
    for uid in ${DIRECT_UIDS}; do
        echo "-A ${CHAIN_APP} -m owner --uid-owner ${uid} -j RETURN"
    done
    echo "-A ${CHAIN_APP} -j MARK --set-mark ${FWMARK}"
elif [ "${APP_MODE}" = "all" ]; then
    echo "-A ${CHAIN_APP} -j MARK --set-mark ${FWMARK}"
fi)
-A ${CHAIN_APP} -j RETURN

-A ${CHAIN_OUT} -m owner --gid-owner ${CORE_GID} -j RETURN
-A ${CHAIN_OUT} -m mark --mark 0xff -j RETURN
-A ${CHAIN_OUT} -p icmpv6 -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${TPROXY_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p udp --dport ${TPROXY_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${DNS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p udp --dport ${DNS_PORT} -m owner ! --uid-owner 0 ! --gid-owner ${CORE_GID} -j DROP
-A ${CHAIN_OUT} -p tcp --dport ${TPROXY_PORT} -j RETURN
-A ${CHAIN_OUT} -p udp --dport ${TPROXY_PORT} -j RETURN
-A ${CHAIN_OUT} -p tcp --dport ${DNS_PORT} -j RETURN
-A ${CHAIN_OUT} -p udp --dport ${DNS_PORT} -j RETURN
$(emit_api_port_protection "${CHAIN_OUT}")
$(emit_socks_port_protection "${CHAIN_OUT}")
$(emit_http_port_protection "${CHAIN_OUT}")
$(emit_chain_proxy_port_protection "${CHAIN_OUT}")

$(for uid in ${BYPASS_UIDS}; do
    echo "-A ${CHAIN_OUT} -m owner --uid-owner ${uid} -j RETURN"
done)

-A ${CHAIN_OUT} -j ${CHAIN_BYPASS}
-A ${CHAIN_OUT} -j ${CHAIN_APP}

-A ${CHAIN_PRE} -p tcp -m socket --transparent -j ${CHAIN_DIVERT}
-A ${CHAIN_PRE} -p udp -m socket --transparent -j ${CHAIN_DIVERT}
-A ${CHAIN_PRE} -j ${CHAIN_BYPASS}
-A ${CHAIN_PRE} -p tcp -m mark --mark ${FWMARK} -j TPROXY --on-ip ::1 --on-port ${TPROXY_PORT} --tproxy-mark ${FWMARK}
-A ${CHAIN_PRE} -p udp -m mark --mark ${FWMARK} -j TPROXY --on-ip ::1 --on-port ${TPROXY_PORT} --tproxy-mark ${FWMARK}

-A OUTPUT -j ${CHAIN_OUT}
-A PREROUTING -j ${CHAIN_PRE}

COMMIT
MANGLE_V6_EOF
}

gen_nat_v4() {
    cat <<NAT_V4_EOF
*nat

:${CHAIN_DNS} - [0:0]

-A ${CHAIN_DNS} -m owner --gid-owner ${CORE_GID} -j RETURN
$(for uid in ${BYPASS_UIDS}; do
    echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -j RETURN"
done)
$(if [ "${DNS_SCOPE}" = "uids" ]; then
    for uid in ${PROXY_UIDS}; do
        echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
        echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    done
elif [ "${DNS_SCOPE}" = "all_except_uids" ]; then
    for uid in ${DIRECT_UIDS}; do
        echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -j RETURN"
    done
    echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
elif [ "${DNS_SCOPE}" = "all" ]; then
    echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
fi)

-A OUTPUT -j ${CHAIN_DNS}

COMMIT
NAT_V4_EOF
}

gen_nat_v6() {
    cat <<NAT_V6_EOF
*nat

:${CHAIN_DNS} - [0:0]

-A ${CHAIN_DNS} -m owner --gid-owner ${CORE_GID} -j RETURN
$(for uid in ${BYPASS_UIDS}; do
    echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -j RETURN"
done)
$(if [ "${DNS_SCOPE}" = "uids" ]; then
    for uid in ${PROXY_UIDS}; do
        echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
        echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    done
elif [ "${DNS_SCOPE}" = "all_except_uids" ]; then
    for uid in ${DIRECT_UIDS}; do
        echo "-A ${CHAIN_DNS} -m owner --uid-owner ${uid} -j RETURN"
    done
    echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
elif [ "${DNS_SCOPE}" = "all" ]; then
    echo "-A ${CHAIN_DNS} -p udp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
    echo "-A ${CHAIN_DNS} -p tcp --dport 53 -j REDIRECT --to-ports ${DNS_PORT}"
fi)

-A OUTPUT -j ${CHAIN_DNS}

COMMIT
NAT_V6_EOF
}
