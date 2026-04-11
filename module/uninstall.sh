#!/system/bin/sh
# PrivStack — uninstall.sh
# Runs when the module is removed via Magisk/KSU/APatch manager.
# Cleans up daemon, iptables rules, ip rules/routes, DNS settings.
# POSIX sh compatible (busybox ash).

# ============================================================================
# Constants
# ============================================================================

PRIVSTACK_DIR="/data/adb/privstack"
PRIVD_PID_FILE="${PRIVSTACK_DIR}/run/privd.pid"
TAG="privstack:uninstall"

# iptables chain prefix — all our chains use this
CHAIN_PREFIX="PRIVSTACK_"

# ip rule/route marks and table used by tproxy setup
TPROXY_MARK="0x2023"
TPROXY_TABLE="2023"
TPROXY_TABLE_V6="2024"

# ============================================================================
# Logging
# ============================================================================

log_msg() {
    /system/bin/log -t "$TAG" -p i "$1" 2>/dev/null
    echo "[privstack] $1"
}

log_err() {
    /system/bin/log -t "$TAG" -p e "$1" 2>/dev/null
    echo "[privstack] ERROR: $1"
}

# ============================================================================
# 1. Stop daemon — SIGTERM first, then SIGKILL if needed
# ============================================================================

stop_daemon() {
    log_msg "Stopping PrivStack daemon..."

    KILLED=0

    # Try PID file first
    if [ -f "$PRIVD_PID_FILE" ]; then
        DAEMON_PID="$(cat "$PRIVD_PID_FILE" 2>/dev/null)"
        if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
            log_msg "Sending SIGTERM to daemon PID ${DAEMON_PID}"
            kill -TERM "$DAEMON_PID" 2>/dev/null
            KILLED=1
        fi
    fi

    # Also find by process name
    for proc_name in privd sing-box; do
        PIDS="$(pidof "$proc_name" 2>/dev/null)"
        if [ -n "$PIDS" ]; then
            for pid in $PIDS; do
                if kill -0 "$pid" 2>/dev/null; then
                    log_msg "Sending SIGTERM to ${proc_name} PID ${pid}"
                    kill -TERM "$pid" 2>/dev/null
                    KILLED=1
                fi
            done
        fi
    done

    if [ "$KILLED" -eq 0 ]; then
        log_msg "No running daemon processes found"
        return
    fi

    # Wait up to 10 seconds for graceful shutdown
    log_msg "Waiting up to 10s for graceful shutdown..."
    WAIT=0
    while [ "$WAIT" -lt 10 ]; do
        ALL_DEAD=1
        for proc_name in privd sing-box; do
            if pidof "$proc_name" >/dev/null 2>&1; then
                ALL_DEAD=0
                break
            fi
        done
        if [ "$ALL_DEAD" -eq 1 ]; then
            log_msg "All processes stopped gracefully"
            return
        fi
        sleep 1
        WAIT=$((WAIT + 1))
    done

    # Force kill survivors
    log_msg "Graceful shutdown timed out — sending SIGKILL"
    for proc_name in privd sing-box; do
        PIDS="$(pidof "$proc_name" 2>/dev/null)"
        if [ -n "$PIDS" ]; then
            for pid in $PIDS; do
                kill -KILL "$pid" 2>/dev/null
                log_msg "SIGKILL sent to ${proc_name} PID ${pid}"
            done
        fi
    done

    sleep 1
}

# ============================================================================
# 2. Flush iptables chains — both IPv4 and IPv6
# ============================================================================

flush_iptables() {
    log_msg "Flushing PrivStack iptables rules..."

    for cmd in iptables ip6tables; do
        # Check if the command exists
        if ! command -v "$cmd" >/dev/null 2>&1; then
            log_msg "${cmd} not found — skipping"
            continue
        fi

        # Process mangle and nat tables
        for table in mangle nat; do
            # List all chains in this table
            CHAINS="$($cmd -t "$table" -L -n 2>/dev/null | grep "^Chain ${CHAIN_PREFIX}" | awk '{print $2}')"

            if [ -z "$CHAINS" ]; then
                continue
            fi

            for chain in $CHAINS; do
                # First, remove all references to this chain from built-in chains
                for builtin in PREROUTING OUTPUT POSTROUTING INPUT FORWARD; do
                    # Find and delete jump rules pointing to our chain
                    RULE_NUMS="$($cmd -t "$table" -L "$builtin" --line-numbers -n 2>/dev/null | grep "$chain" | awk '{print $1}' | sort -rn)"
                    for num in $RULE_NUMS; do
                        $cmd -t "$table" -D "$builtin" "$num" 2>/dev/null
                        log_msg "${cmd} -t ${table}: removed rule #${num} from ${builtin} -> ${chain}"
                    done
                done

                # Flush the chain
                $cmd -t "$table" -F "$chain" 2>/dev/null
                log_msg "${cmd} -t ${table}: flushed chain ${chain}"

                # Delete the chain
                $cmd -t "$table" -X "$chain" 2>/dev/null
                log_msg "${cmd} -t ${table}: deleted chain ${chain}"
            done
        done

        # Also clean up any stray rules with our mark in built-in chains
        for table in mangle nat filter; do
            for builtin in PREROUTING OUTPUT POSTROUTING INPUT FORWARD; do
                # Delete rules containing our mark or comment
                while $cmd -t "$table" -L "$builtin" -n 2>/dev/null | grep -q "PRIVSTACK\|0x2023\|privstack"; do
                    RULE_NUM="$($cmd -t "$table" -L "$builtin" --line-numbers -n 2>/dev/null | grep "PRIVSTACK\|0x2023\|privstack" | head -1 | awk '{print $1}')"
                    if [ -n "$RULE_NUM" ]; then
                        $cmd -t "$table" -D "$builtin" "$RULE_NUM" 2>/dev/null
                        log_msg "${cmd} -t ${table}: removed stray rule #${RULE_NUM} from ${builtin}"
                    else
                        break
                    fi
                done
            done
        done
    done

    log_msg "iptables cleanup complete"
}

# ============================================================================
# 3. Remove ip rule and route entries — both IPv4 and IPv6
# ============================================================================

flush_ip_rules_routes() {
    log_msg "Removing PrivStack ip rules and routes..."

    # IPv4: use TPROXY_TABLE (2023)
    ATTEMPTS=0
    while [ "$ATTEMPTS" -lt 20 ]; do
        if ip rule show 2>/dev/null | grep -q "fwmark ${TPROXY_MARK}\|fwmark 0x2023"; then
            ip rule del fwmark "$TPROXY_MARK" table "$TPROXY_TABLE" 2>/dev/null
            ip rule del fwmark "$TPROXY_MARK" 2>/dev/null
            ATTEMPTS=$((ATTEMPTS + 1))
        else
            break
        fi
    done
    ATTEMPTS=0
    while [ "$ATTEMPTS" -lt 20 ]; do
        if ip rule show 2>/dev/null | grep -q "lookup ${TPROXY_TABLE}"; then
            ip rule del table "$TPROXY_TABLE" 2>/dev/null
            ATTEMPTS=$((ATTEMPTS + 1))
        else
            break
        fi
    done
    ip route flush table "$TPROXY_TABLE" 2>/dev/null

    # IPv6: use TPROXY_TABLE_V6 (2024)
    ATTEMPTS=0
    while [ "$ATTEMPTS" -lt 20 ]; do
        if ip -6 rule show 2>/dev/null | grep -q "fwmark ${TPROXY_MARK}\|fwmark 0x2023"; then
            ip -6 rule del fwmark "$TPROXY_MARK" table "$TPROXY_TABLE_V6" 2>/dev/null
            ip -6 rule del fwmark "$TPROXY_MARK" 2>/dev/null
            ATTEMPTS=$((ATTEMPTS + 1))
        else
            break
        fi
    done
    ATTEMPTS=0
    while [ "$ATTEMPTS" -lt 20 ]; do
        if ip -6 rule show 2>/dev/null | grep -q "lookup ${TPROXY_TABLE_V6}"; then
            ip -6 rule del table "$TPROXY_TABLE_V6" 2>/dev/null
            ATTEMPTS=$((ATTEMPTS + 1))
        else
            break
        fi
    done
    ip -6 route flush table "$TPROXY_TABLE_V6" 2>/dev/null

    log_msg "ip rules/routes cleanup complete"
}

# ============================================================================
# 4. Restore Private DNS if we changed it
# ============================================================================

restore_private_dns() {
    log_msg "Checking Private DNS state..."

    PRIV_DNS_BAK="${PRIVSTACK_DIR}/backup/private_dns_mode"
    PRIV_DNS_SPEC_BAK="${PRIVSTACK_DIR}/backup/private_dns_specifier"

    if [ -f "$PRIV_DNS_BAK" ]; then
        ORIG_MODE="$(cat "$PRIV_DNS_BAK" 2>/dev/null)"
        if [ -n "$ORIG_MODE" ]; then
            settings put global private_dns_mode "$ORIG_MODE" 2>/dev/null
            log_msg "Restored private_dns_mode to: ${ORIG_MODE}"
        fi
    fi

    if [ -f "$PRIV_DNS_SPEC_BAK" ]; then
        ORIG_SPEC="$(cat "$PRIV_DNS_SPEC_BAK" 2>/dev/null)"
        if [ -n "$ORIG_SPEC" ]; then
            settings put global private_dns_specifier "$ORIG_SPEC" 2>/dev/null
            log_msg "Restored private_dns_specifier to: ${ORIG_SPEC}"
        fi
    fi
}

# ============================================================================
# 5. Restore kernel parameters
# ============================================================================

restore_kernel_params() {
    log_msg "Restoring kernel parameters..."

    # Restore rp_filter to default (1 = strict)
    for rp_path in /proc/sys/net/ipv4/conf/all/rp_filter \
                   /proc/sys/net/ipv4/conf/default/rp_filter; do
        if [ -f "$rp_path" ]; then
            echo 1 > "$rp_path" 2>/dev/null
        fi
    done

    log_msg "Kernel parameters restored"
}

# ============================================================================
# 6. Clean runtime files (preserve config and logs for forensics)
# ============================================================================

clean_runtime() {
    log_msg "Cleaning runtime files..."

    # Remove PID files and sockets
    rm -f "${PRIVSTACK_DIR}/run/"* 2>/dev/null

    # Remove rendered configs (they are generated, not user-created)
    rm -f "${PRIVSTACK_DIR}/config/rendered/"* 2>/dev/null

    log_msg "Runtime files cleaned"
}

# ============================================================================
# Main uninstall flow
# ============================================================================

log_msg "========================================="
log_msg "PrivStack module removal starting"
log_msg "========================================="

# Step 1: Stop all daemon processes
stop_daemon

# Step 2: Flush all iptables rules in our chains
flush_iptables

# Step 3: Remove ip rule/route entries
flush_ip_rules_routes

# Step 4: Restore Private DNS setting
restore_private_dns

# Step 5: Restore kernel parameters
restore_kernel_params

# Step 6: Clean runtime artifacts
clean_runtime

log_msg "========================================="
log_msg "PrivStack module removal complete"
log_msg "Data directory preserved at: ${PRIVSTACK_DIR}/"
log_msg "Remove manually if no longer needed:"
log_msg "  rm -rf ${PRIVSTACK_DIR}"
log_msg "========================================="
