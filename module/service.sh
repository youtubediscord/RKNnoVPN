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

TAG="privstack:service"
BOOT_TIMEOUT=120
SETTLE_DELAY=5

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

# ============================================================================
# 1. Check manual flag — skip autostart if set
# ============================================================================

if [ -f "$MANUAL_FLAG" ]; then
    log_info "Manual flag detected at ${MANUAL_FLAG} — skipping autostart"
    log_info "Remove the flag and run privctl start to launch manually"
    exit 0
fi

# ============================================================================
# 2. Detect root manager and set busybox path
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

detect_busybox
log_info "Busybox: ${BUSYBOX:-not found}"

# ============================================================================
# 3. Wait for boot completion
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
# 8. Set OOM score — protect daemon from being killed under memory pressure
# ============================================================================

if [ "$LAUNCH_RESULT" -eq 0 ]; then
    DAEMON_PID="$(cat "$PRIVD_PID_FILE" 2>/dev/null)"
    if [ -n "$DAEMON_PID" ] && [ -d "/proc/${DAEMON_PID}" ]; then
        # oom_score_adj range: -1000 to 1000
        # -500 provides strong OOM protection without full immunity
        echo -500 > "/proc/${DAEMON_PID}/oom_score_adj" 2>/dev/null
        if [ $? -eq 0 ]; then
            log_info "Set oom_score_adj=-500 for PID ${DAEMON_PID}"
        else
            log_warn "Failed to set oom_score_adj for PID ${DAEMON_PID}"
        fi

        # Also protect sing-box child if it exists
        sleep 3
        SINGBOX_PID="$(pidof sing-box 2>/dev/null | awk '{print $1}')"
        if [ -n "$SINGBOX_PID" ] && [ -d "/proc/${SINGBOX_PID}" ]; then
            echo -500 > "/proc/${SINGBOX_PID}/oom_score_adj" 2>/dev/null
            log_info "Set oom_score_adj=-500 for sing-box PID ${SINGBOX_PID}"
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
