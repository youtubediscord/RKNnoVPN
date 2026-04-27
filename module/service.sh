#!/system/bin/sh
# PrivStack — service.sh
# Runs at boot after data is decrypted (non-blocking).
# Launches the privd daemon that manages sing-box + iptables.
# POSIX sh compatible (busybox ash).

# ============================================================================
# Constants
# ============================================================================

MODDIR="${0%/*}"
PRIVSTACK_DIR="/data/adb/privstack"
PRIVSTACK_GID=23333

PRIVD_BIN="${PRIVSTACK_DIR}/bin/privd"
PRIVD_PID_FILE="${PRIVSTACK_DIR}/run/privd.pid"
PRIVD_SOCK="${PRIVSTACK_DIR}/run/daemon.sock"
CONFIG_FILE="${PRIVSTACK_DIR}/config/config.json"
MANUAL_FLAG="${PRIVSTACK_DIR}/config/manual"
LOG_FILE="${PRIVSTACK_DIR}/logs/service.log"
MODULE_PROP="${MODDIR}/module.prop"
LOG_VERSION_FILE="${PRIVSTACK_DIR}/logs/.version"
LOG_ARCHIVE_DIR="${PRIVSTACK_DIR}/logs/archive"

TAG="privstack:service"
BOOT_TIMEOUT=120
SETTLE_DELAY=5
OOM_SCORE_ADJ="${PRIVSTACK_OOM_SCORE_ADJ:-300}"

if [ -f "${PRIVSTACK_DIR}/scripts/lib/privstack_env.sh" ]; then
    . "${PRIVSTACK_DIR}/scripts/lib/privstack_env.sh"
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
    mkdir -p "${PRIVSTACK_DIR}/logs" 2>/dev/null || return 0
    chown 0:0 "${PRIVSTACK_DIR}/logs" 2>/dev/null
    chmod 0700 "${PRIVSTACK_DIR}/logs" 2>/dev/null

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

    for name in privd.log sing-box.log service.log rescue_reset.log net_change.log; do
        src="${PRIVSTACK_DIR}/logs/${name}"
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
    if command -v privstack_has_boot_cleanup_markers >/dev/null 2>&1; then
        privstack_has_boot_cleanup_markers
        return $?
    fi
    for marker in \
        "$PRIVSTACK_DIR/run/active" \
        "$PRIVSTACK_DIR/run/reset.lock" \
        "$PRIVSTACK_DIR/run/privd.pid" \
        "$PRIVSTACK_DIR/run/singbox.pid" \
        "$PRIVSTACK_DIR/run/daemon.sock" \
        "$PRIVSTACK_DIR/run/env.sh" \
        "$PRIVSTACK_DIR/run/iptables.rules" \
        "$PRIVSTACK_DIR/run/ip6tables.rules" \
        "$PRIVSTACK_DIR/run/iptables_backup.rules" \
        "$PRIVSTACK_DIR/run/ip6tables_backup.rules"; do
        [ -e "$marker" ] && return 0
    done
    return 1
}

if [ -x "${PRIVSTACK_DIR}/scripts/rescue_reset.sh" ]; then
    if has_boot_cleanup_markers; then
        log_info "Running boot rescue cleanup"
        if "${PRIVSTACK_DIR}/scripts/rescue_reset.sh" --boot-clean >> "$LOG_FILE" 2>&1; then
            log_info "Boot rescue cleanup completed"
        else
            log_warn "Boot rescue cleanup reported leftovers; daemon will still start for diagnostics"
        fi
    else
        log_info "Skipping boot rescue cleanup: no stale runtime markers"
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
if [ ! -x "$PRIVD_BIN" ]; then
    log_error "Daemon binary not found or not executable: ${PRIVD_BIN}"
    log_error "Install sing-box/privd to ${PRIVSTACK_DIR}/bin/ and reboot"
    exit 1
fi

# Check that config exists
if [ ! -f "$CONFIG_FILE" ]; then
    log_error "Config file not found: ${CONFIG_FILE}"
    log_error "Copy config.json to ${PRIVSTACK_DIR}/config/ and reboot"
    exit 1
fi

# ============================================================================
# 5. Kill stale daemon and socket
# ============================================================================

kill_stale() {
    # Kill by PID file
    if [ -f "$PRIVD_PID_FILE" ]; then
        STALE_PID="$(cat "$PRIVD_PID_FILE" 2>/dev/null)"
        if [ -n "$STALE_PID" ]; then
            if kill -0 "$STALE_PID" 2>/dev/null; then
                log_info "Killing stale daemon PID ${STALE_PID}"
                kill -TERM "$STALE_PID" 2>/dev/null
                # Brief wait for graceful shutdown
                WAIT=0
                while [ "$WAIT" -lt 5 ] && kill -0 "$STALE_PID" 2>/dev/null; do
                    sleep 1
                    WAIT=$((WAIT + 1))
                done
                # Force kill if still alive
                if kill -0 "$STALE_PID" 2>/dev/null; then
                    kill -KILL "$STALE_PID" 2>/dev/null
                    log_warn "Force-killed stale daemon PID ${STALE_PID}"
                fi
            fi
        fi
        rm -f "$PRIVD_PID_FILE" 2>/dev/null
    fi

    # Kill by process name as fallback
    for proc_name in privd; do
        PIDS="$(pidof "$proc_name" 2>/dev/null)"
        if [ -n "$PIDS" ]; then
            log_info "Killing stale ${proc_name} processes: ${PIDS}"
            for pid in $PIDS; do
                kill -TERM "$pid" 2>/dev/null
            done
            sleep 2
            # Force kill survivors
            PIDS="$(pidof "$proc_name" 2>/dev/null)"
            if [ -n "$PIDS" ]; then
                for pid in $PIDS; do
                    kill -KILL "$pid" 2>/dev/null
                done
            fi
        fi
    done

    # Remove stale socket
    rm -f "$PRIVD_SOCK" 2>/dev/null

    # Remove stale PID files
    rm -f "${PRIVSTACK_DIR}/run/"*.pid 2>/dev/null
}

kill_stale

# ============================================================================
# 6. Set resource limits
# ============================================================================

# Raise file descriptor limit — sing-box may handle thousands of connections
ulimit -SHn 1000000 2>/dev/null
ACTUAL_ULIMIT="$(ulimit -n 2>/dev/null)"
log_info "File descriptor limit: ${ACTUAL_ULIMIT}"

# ============================================================================
# 7. Launch daemon
# ============================================================================

launch_daemon() {
    log_info "Launching privd daemon..."
    log_info "  Binary:  ${PRIVD_BIN}"
    log_info "  Config:  ${CONFIG_FILE}"
    log_info "  PID file: ${PRIVD_PID_FILE}"

    # Ensure log directory is writable
    mkdir -p "${PRIVSTACK_DIR}/logs" 2>/dev/null
    chown 0:0 "${PRIVSTACK_DIR}/logs" 2>/dev/null
    chmod 0700 "${PRIVSTACK_DIR}/logs" 2>/dev/null
    touch "${PRIVSTACK_DIR}/logs/privd.log" 2>/dev/null
    chown 0:0 "${PRIVSTACK_DIR}/logs/privd.log" 2>/dev/null
    chmod 0600 "${PRIVSTACK_DIR}/logs/privd.log" 2>/dev/null

    # Launch daemon with nohup + setsid to fully detach from init
    # - nohup: ignore SIGHUP when terminal closes
    # - setsid: create new session (no controlling terminal)
    # stdout/stderr go to daemon log file
    nohup setsid "${PRIVD_BIN}" \
        --config "${CONFIG_FILE}" \
        --data-dir "${PRIVSTACK_DIR}" \
        >> "${PRIVSTACK_DIR}/logs/privd.log" 2>&1 &

    DAEMON_PID=$!

    # Brief wait to check if it crashed immediately
    sleep 2

    # Check if process is still alive
    if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
        # Process died — try to find the actual PID (setsid may have forked)
        DAEMON_PID="$(pidof privd 2>/dev/null | awk '{print $1}')"
        if [ -z "$DAEMON_PID" ]; then
            log_error "Daemon failed to start — check ${PRIVSTACK_DIR}/logs/privd.log"
            return 1
        fi
    fi

    # Write PID file
    echo "$DAEMON_PID" > "$PRIVD_PID_FILE" 2>/dev/null
    log_info "Daemon started with PID ${DAEMON_PID}"

    return 0
}

launch_daemon
LAUNCH_RESULT=$?

# ============================================================================
# 8. Set OOM score — keep Android UI preferred over PrivStack workers
# ============================================================================

if [ "$LAUNCH_RESULT" -eq 0 ]; then
    DAEMON_PID="$(cat "$PRIVD_PID_FILE" 2>/dev/null)"
    if [ -n "$DAEMON_PID" ] && [ -d "/proc/${DAEMON_PID}" ]; then
        # oom_score_adj range: -1000 to 1000
        # Positive values make PrivStack easier to reclaim than SystemUI.
        echo "$OOM_SCORE_ADJ" > "/proc/${DAEMON_PID}/oom_score_adj" 2>/dev/null
        if [ $? -eq 0 ]; then
            log_info "Set oom_score_adj=${OOM_SCORE_ADJ} for PID ${DAEMON_PID}"
        else
            log_warn "Failed to set oom_score_adj for PID ${DAEMON_PID}"
        fi

        # Apply the same non-critical priority to sing-box if it exists.
        sleep 3
        SINGBOX_PID="$(pidof sing-box 2>/dev/null | awk '{print $1}')"
        if [ -n "$SINGBOX_PID" ] && [ -d "/proc/${SINGBOX_PID}" ]; then
            echo "$OOM_SCORE_ADJ" > "/proc/${SINGBOX_PID}/oom_score_adj" 2>/dev/null
            log_info "Set oom_score_adj=${OOM_SCORE_ADJ} for sing-box PID ${SINGBOX_PID}"
        fi
    fi
fi

# ============================================================================
# 9. Final verification
# ============================================================================

if [ "$LAUNCH_RESULT" -eq 0 ]; then
    # Final check after OOM adjustment
    DAEMON_PID="$(cat "$PRIVD_PID_FILE" 2>/dev/null)"
    if [ -n "$DAEMON_PID" ] && kill -0 "$DAEMON_PID" 2>/dev/null; then
        log_info "PrivStack daemon is running (PID ${DAEMON_PID})"
        log_info "service.sh completed successfully"
    else
        log_error "Daemon PID ${DAEMON_PID} is no longer running"
        log_error "Check logs at ${PRIVSTACK_DIR}/logs/privd.log"
        exit 1
    fi
else
    log_error "Failed to launch daemon"
    log_error "Check logs at ${PRIVSTACK_DIR}/logs/privd.log"
    exit 1
fi
