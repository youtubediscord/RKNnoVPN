#!/system/bin/sh
# RKNnoVPN — service.sh
# Runs at boot after data is decrypted (non-blocking).
# Launches the RKNnoVPN daemon that manages sing-box + iptables.
# POSIX sh compatible (busybox ash).

# ============================================================================
# Constants
# ============================================================================

MODDIR="${0%/*}"
RKNNOVPN_DIR="${RKNNOVPN_DIR:-${MODDIR:-/data/adb/modules/rknnovpn}}"
RKNNOVPN_GID=23333

DAEMON_BIN="${RKNNOVPN_DIR}/bin/daemon"
DAEMON_PID_FILE="${RKNNOVPN_DIR}/run/daemon.pid"
CONFIG_FILE="${RKNNOVPN_DIR}/config/config.json"
MANUAL_FLAG="${RKNNOVPN_DIR}/config/manual"
LOG_FILE="${RKNNOVPN_DIR}/logs/service.log"
MODULE_PROP="${MODDIR}/module.prop"
LOG_VERSION_FILE="${RKNNOVPN_DIR}/logs/.version"
LOG_ARCHIVE_DIR="${RKNNOVPN_DIR}/logs/archive"

TAG="rknnovpn:service"
BOOT_TIMEOUT=120
SETTLE_DELAY=5
OOM_SCORE_ADJ="${RKNNOVPN_OOM_SCORE_ADJ:-300}"

if [ -f "${RKNNOVPN_DIR}/scripts/lib/rknnovpn_env.sh" ]; then
    . "${RKNNOVPN_DIR}/scripts/lib/rknnovpn_env.sh"
fi

# ============================================================================
# Logging
# ============================================================================

ts() {
    date "+%Y-%m-%d %H:%M:%S" 2>/dev/null || echo "----"
}

log_info() {
    msg="$(ts) [INFO] $1"
    echo "$msg" >> "$LOG_FILE" 2>/dev/null
    /system/bin/log -t "$TAG" -p i "$1" 2>/dev/null
}

log_warn() {
    msg="$(ts) [WARN] $1"
    echo "$msg" >> "$LOG_FILE" 2>/dev/null
    /system/bin/log -t "$TAG" -p w "$1" 2>/dev/null
}

log_error() {
    msg="$(ts) [ERROR] $1"
    echo "$msg" >> "$LOG_FILE" 2>/dev/null
    /system/bin/log -t "$TAG" -p e "$1" 2>/dev/null
}

module_version() {
    if [ -f "$MODULE_PROP" ]; then
        sed -n 's/^version=//p' "$MODULE_PROP" 2>/dev/null | head -n 1
    fi
}

rotate_logs_if_version_changed() {
    mkdir -p "${RKNNOVPN_DIR}/logs" 2>/dev/null || return 0
    chown 0:0 "${RKNNOVPN_DIR}/logs" 2>/dev/null
    chmod 0700 "${RKNNOVPN_DIR}/logs" 2>/dev/null

    CURRENT_VERSION="$(module_version)"
    [ -n "$CURRENT_VERSION" ] || return 0

    PREVIOUS_VERSION=""
    if [ -f "$LOG_VERSION_FILE" ]; then
        PREVIOUS_VERSION="$(sed -n '1p' "$LOG_VERSION_FILE" 2>/dev/null)"
    fi
    if [ "$PREVIOUS_VERSION" = "$CURRENT_VERSION" ]; then
        return 0
    fi

    STAMP="$(date "+%Y%m%d-%H%M%S" 2>/dev/null || echo "now")"
    FROM_VERSION="${PREVIOUS_VERSION:-unknown}"
    ARCHIVE_DIR="${LOG_ARCHIVE_DIR}/${FROM_VERSION}_to_${CURRENT_VERSION}_${STAMP}"
    MOVED=0

    for name in daemon.log sing-box.log service.log rescue_reset.log net_change.log; do
        src="${RKNNOVPN_DIR}/logs/${name}"
        if [ -s "$src" ]; then
            mkdir -p "$ARCHIVE_DIR" 2>/dev/null || continue
            if mv "$src" "${ARCHIVE_DIR}/${name}" 2>/dev/null; then
                MOVED=1
            fi
        fi
    done

    if [ "$MOVED" = "1" ]; then
        chown -R 0:0 "$ARCHIVE_DIR" 2>/dev/null
        chmod 0700 "$ARCHIVE_DIR" 2>/dev/null
        for f in "$ARCHIVE_DIR"/*; do
            [ -f "$f" ] && chmod 0600 "$f" 2>/dev/null
        done
        echo "$(ts) [INFO] Rotated logs for module ${FROM_VERSION} -> ${CURRENT_VERSION}: ${ARCHIVE_DIR}" >> "$LOG_FILE" 2>/dev/null
    fi

    echo "$CURRENT_VERSION" > "$LOG_VERSION_FILE" 2>/dev/null
    chown 0:0 "$LOG_VERSION_FILE" 2>/dev/null
    chmod 0600 "$LOG_VERSION_FILE" 2>/dev/null
}

# ============================================================================
# 1. Detect root manager and set busybox path
# ============================================================================

detect_busybox() {
    # KernelSU
    if [ -n "$KSU" ] && [ "$KSU" = "true" ]; then
        if [ -x "/data/adb/ksu/bin/busybox" ]; then
            BUSYBOX="/data/adb/ksu/bin/busybox"
            return
        fi
    fi

    # APatch
    if [ -n "$APATCH" ] && [ "$APATCH" = "true" ]; then
        if [ -x "/data/adb/ap/bin/busybox" ]; then
            BUSYBOX="/data/adb/ap/bin/busybox"
            return
        fi
    fi

    # Magisk
    if [ -x "/data/adb/magisk/busybox" ]; then
        BUSYBOX="/data/adb/magisk/busybox"
        return
    fi

    # System busybox
    if command -v busybox >/dev/null 2>&1; then
        BUSYBOX="busybox"
        return
    fi

    # Fallback — no busybox, use shell builtins only
    BUSYBOX=""
}

rotate_logs_if_version_changed
detect_busybox
log_info "Busybox: ${BUSYBOX:-not found}"

# ============================================================================
# 2. Wait for boot completion
# ============================================================================

wait_boot_completed() {
    log_info "Waiting for sys.boot_completed (timeout: ${BOOT_TIMEOUT}s)..."

    ELAPSED=0
    while [ "$ELAPSED" -lt "$BOOT_TIMEOUT" ]; do
        BOOT_DONE="$(getprop sys.boot_completed 2>/dev/null)"
        if [ "$BOOT_DONE" = "1" ]; then
            log_info "Boot completed after ${ELAPSED}s"
            return 0
        fi
        sleep 1
        ELAPSED=$((ELAPSED + 1))
    done

    log_warn "Boot completion timeout after ${BOOT_TIMEOUT}s — proceeding anyway"
    return 1
}

wait_boot_completed

# Post-boot settle delay — let other services finish starting
log_info "Settle delay: ${SETTLE_DELAY}s"
sleep "$SETTLE_DELAY"

# ============================================================================
# 3. Boot rescue cleanup
# ============================================================================

has_boot_cleanup_markers() {
    if command -v rknnovpn_has_boot_cleanup_markers >/dev/null 2>&1; then
        rknnovpn_has_boot_cleanup_markers
        return $?
    fi
    log_warn "rknnovpn_env.sh marker helper unavailable; boot cleanup will run as probe"
    return 1
}

if [ -x "${RKNNOVPN_DIR}/scripts/rescue_reset.sh" ]; then
    if has_boot_cleanup_markers; then
        log_info "Running boot rescue cleanup"
    else
        log_info "Running boot rescue cleanup probe without stale runtime markers"
    fi
    if "${RKNNOVPN_DIR}/scripts/rescue_reset.sh" --boot-clean >> "$LOG_FILE" 2>&1; then
        log_info "Boot rescue cleanup completed"
    else
        log_warn "Boot rescue cleanup reported leftovers; daemon will still start for diagnostics"
    fi
else
    log_warn "Boot rescue cleanup script not found"
fi

if [ -f "$MANUAL_FLAG" ]; then
    log_info "Manual flag detected at ${MANUAL_FLAG} — daemon will start, proxy autostart stays disabled"
fi

# ============================================================================
# 4. Pre-launch validation
# ============================================================================

# Check that the daemon binary exists
if [ ! -x "$DAEMON_BIN" ]; then
    log_error "Daemon binary not found or not executable: ${DAEMON_BIN}"
    log_error "Install sing-box/daemon to ${RKNNOVPN_DIR}/bin/ and reboot"
    exit 1
fi

# Check that config exists
if [ ! -f "$CONFIG_FILE" ]; then
    log_error "Config file not found: ${CONFIG_FILE}"
    log_error "Copy config.json to ${RKNNOVPN_DIR}/config/ and reboot"
    exit 1
fi

first_pid_by_cmd_path() {
    wanted="$1"
    for p in /proc/[0-9]*; do
        pid="${p##*/}"
        [ "$pid" = "$$" ] && continue
        cmd="$(tr '\000' ' ' < "$p/cmdline" 2>/dev/null)"
        case "$cmd" in
            *"$wanted"*)
                echo "$pid"
                return 0
                ;;
        esac
    done
    return 1
}

# ============================================================================
# 5. Set resource limits
# ============================================================================

# Raise file descriptor limit — sing-box may handle thousands of connections
ulimit -SHn 1000000 2>/dev/null
ACTUAL_ULIMIT="$(ulimit -n 2>/dev/null)"
log_info "File descriptor limit: ${ACTUAL_ULIMIT}"

# ============================================================================
# 6. Launch daemon
# ============================================================================

launch_daemon() {
    log_info "Launching RKNnoVPN daemon..."
    log_info "  Binary:  ${DAEMON_BIN}"
    log_info "  Config:  ${CONFIG_FILE}"
    log_info "  PID file: ${DAEMON_PID_FILE}"

    # Ensure log directory is writable
    mkdir -p "${RKNNOVPN_DIR}/logs" 2>/dev/null
    chown 0:0 "${RKNNOVPN_DIR}/logs" 2>/dev/null
    chmod 0700 "${RKNNOVPN_DIR}/logs" 2>/dev/null
    touch "${RKNNOVPN_DIR}/logs/daemon.log" 2>/dev/null
    chown 0:0 "${RKNNOVPN_DIR}/logs/daemon.log" 2>/dev/null
    chmod 0600 "${RKNNOVPN_DIR}/logs/daemon.log" 2>/dev/null

    # Launch daemon with nohup + setsid to fully detach from init
    # - nohup: ignore SIGHUP when terminal closes
    # - setsid: create new session (no controlling terminal)
    # stdout/stderr go to daemon log file
    nohup setsid "${DAEMON_BIN}" \
        --config "${CONFIG_FILE}" \
        --data-dir "${RKNNOVPN_DIR}" \
        >> "${RKNNOVPN_DIR}/logs/daemon.log" 2>&1 &

    DAEMON_PID=$!

    # Brief wait to check if it crashed immediately
    sleep 2

    # Check if process is still alive
    if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
        # Process died — try to find the actual PID (setsid may have forked)
        DAEMON_PID="$(first_pid_by_cmd_path "$DAEMON_BIN" 2>/dev/null)"
        if [ -z "$DAEMON_PID" ]; then
            log_error "Daemon failed to start — check ${RKNNOVPN_DIR}/logs/daemon.log"
            return 1
        fi
    fi

    # Write PID file
    echo "$DAEMON_PID" > "$DAEMON_PID_FILE" 2>/dev/null
    log_info "Daemon started with PID ${DAEMON_PID}"

    return 0
}

launch_daemon
LAUNCH_RESULT=$?

# ============================================================================
# 7. Set OOM score — keep Android UI preferred over RKNnoVPN workers
# ============================================================================

if [ "$LAUNCH_RESULT" -eq 0 ]; then
    DAEMON_PID="$(cat "$DAEMON_PID_FILE" 2>/dev/null)"
    if [ -n "$DAEMON_PID" ] && [ -d "/proc/${DAEMON_PID}" ]; then
        # oom_score_adj range: -1000 to 1000
        # Positive values make RKNnoVPN easier to reclaim than SystemUI.
        echo "$OOM_SCORE_ADJ" > "/proc/${DAEMON_PID}/oom_score_adj" 2>/dev/null
        if [ $? -eq 0 ]; then
            log_info "Set oom_score_adj=${OOM_SCORE_ADJ} for PID ${DAEMON_PID}"
        else
            log_warn "Failed to set oom_score_adj for PID ${DAEMON_PID}"
        fi

        # Apply the same non-critical priority to sing-box if it exists.
        sleep 3
        SINGBOX_PID="$(first_pid_by_cmd_path "${RKNNOVPN_DIR}/bin/sing-box" 2>/dev/null)"
        if [ -n "$SINGBOX_PID" ] && [ -d "/proc/${SINGBOX_PID}" ]; then
            echo "$OOM_SCORE_ADJ" > "/proc/${SINGBOX_PID}/oom_score_adj" 2>/dev/null
            log_info "Set oom_score_adj=${OOM_SCORE_ADJ} for sing-box PID ${SINGBOX_PID}"
        fi
    fi
fi

# ============================================================================
# 8. Final verification
# ============================================================================

if [ "$LAUNCH_RESULT" -eq 0 ]; then
    # Final check after OOM adjustment
    DAEMON_PID="$(cat "$DAEMON_PID_FILE" 2>/dev/null)"
    if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        log_info "RKNnoVPN daemon is running (PID ${DAEMON_PID})"
        log_info "service.sh completed successfully"
    else
        log_error "Daemon PID ${DAEMON_PID} is no longer running"
        log_error "Check logs at ${RKNNOVPN_DIR}/logs/daemon.log"
        exit 1
    fi
else
    log_error "Failed to launch daemon"
    log_error "Check logs at ${RKNNOVPN_DIR}/logs/daemon.log"
    exit 1
fi
