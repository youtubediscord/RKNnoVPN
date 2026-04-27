#!/system/bin/sh
# PrivStack — module removal entrypoint.
# Runtime cleanup is owned by scripts/rescue_reset.sh; this file only
# orchestrates uninstall-specific cleanup and preserves config/logs.

set +e

PRIVSTACK_DIR="${PRIVSTACK_DIR:-/data/adb/privstack}"
TAG="privstack:uninstall"

if [ -f "${PRIVSTACK_DIR}/scripts/lib/privstack_env.sh" ]; then
    . "${PRIVSTACK_DIR}/scripts/lib/privstack_env.sh"
fi
SCRIPTS_DIR="${SCRIPTS_DIR:-${PRIVSTACK_DIR}/scripts}"
RUN_DIR="${RUN_DIR:-${PRIVSTACK_DIR}/run}"
BACKUP_DIR="${BACKUP_DIR:-${PRIVSTACK_DIR}/backup}"
RENDERED_CONFIG_DIR="${RENDERED_CONFIG_DIR:-${PRIVSTACK_DIR}/config/rendered}"

log_msg() {
    /system/bin/log -t "$TAG" -p i "$1" 2>/dev/null
    echo "[privstack] $1"
}

log_err() {
    /system/bin/log -t "$TAG" -p e "$1" 2>/dev/null
    echo "[privstack] ERROR: $1"
}

run_runtime_cleanup() {
    if [ -x "${SCRIPTS_DIR}/rescue_reset.sh" ]; then
        log_msg "Running canonical runtime cleanup"
        PRIVSTACK_DIR="$PRIVSTACK_DIR" "${SCRIPTS_DIR}/rescue_reset.sh" uninstall-clean
        return $?
    fi

    log_err "rescue_reset.sh missing; falling back to process-only cleanup"
    for proc_name in privd sing-box; do
        PIDS="$(pidof "$proc_name" 2>/dev/null)"
        [ -z "$PIDS" ] && continue
        for pid in $PIDS; do
            kill -TERM "$pid" 2>/dev/null
        done
    done
    sleep 2
    for proc_name in privd sing-box; do
        PIDS="$(pidof "$proc_name" 2>/dev/null)"
        [ -z "$PIDS" ] && continue
        for pid in $PIDS; do
            kill -KILL "$pid" 2>/dev/null
        done
    done
    return 1
}

restore_private_dns() {
    log_msg "Checking Private DNS state"

    PRIV_DNS_BAK="${BACKUP_DIR}/private_dns_mode"
    PRIV_DNS_SPEC_BAK="${BACKUP_DIR}/private_dns_specifier"

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

restore_kernel_params() {
    log_msg "Restoring kernel parameters"
    for rp_path in /proc/sys/net/ipv4/conf/all/rp_filter \
                   /proc/sys/net/ipv4/conf/default/rp_filter; do
        if [ -f "$rp_path" ]; then
            echo 1 > "$rp_path" 2>/dev/null
        fi
    done
}

clean_runtime_files() {
    log_msg "Cleaning generated runtime files"
    rm -f "${RUN_DIR}/"* 2>/dev/null
    rm -f "${RENDERED_CONFIG_DIR}/"* 2>/dev/null
}

log_msg "========================================="
log_msg "PrivStack module removal starting"
log_msg "========================================="

run_runtime_cleanup || log_err "Runtime cleanup reported leftovers"
restore_private_dns
restore_kernel_params
clean_runtime_files

log_msg "========================================="
log_msg "PrivStack module removal complete"
log_msg "Data directory preserved at: ${PRIVSTACK_DIR}/"
log_msg "Remove manually if no longer needed:"
log_msg "  rm -rf ${PRIVSTACK_DIR}"
log_msg "========================================="
