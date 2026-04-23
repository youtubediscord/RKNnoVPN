#!/system/bin/sh
# PrivStack — post-fs-data.sh
# Runs early in boot (blocking, before zygote).
# Keep this FAST — heavy work goes in service.sh.
# POSIX sh compatible (busybox ash).

# ============================================================================
# Constants
# ============================================================================

MODDIR="${0%/*}"
PRIVSTACK_DIR="/data/adb/privstack"
PRIVSTACK_GID=23333
TAG="privstack:post-fs-data"

# Subdirectories that must exist
SUBDIRS="bin config config/rendered scripts run logs backup profiles"

# ============================================================================
# Logging
# ============================================================================

log_info() {
    /system/bin/log -t "$TAG" -p i "$1" 2>/dev/null
}

log_warn() {
    /system/bin/log -t "$TAG" -p w "$1" 2>/dev/null
}

log_error() {
    /system/bin/log -t "$TAG" -p e "$1" 2>/dev/null
}

# ============================================================================
# 1. Create directory skeleton if missing
# ============================================================================

log_info "Starting post-fs-data initialization"

for subdir in $SUBDIRS; do
    target="${PRIVSTACK_DIR}/${subdir}"
    if [ ! -d "$target" ]; then
        mkdir -p "$target" 2>/dev/null
        if [ -d "$target" ]; then
            log_info "Created missing directory: ${subdir}"
        else
            log_error "Failed to create directory: ${target}"
        fi
    fi
done

# ============================================================================
# 2. Set file permissions
# ============================================================================

# Binaries: 0750 root:23333
if [ -d "${PRIVSTACK_DIR}/bin" ]; then
    chown 0:${PRIVSTACK_GID} "${PRIVSTACK_DIR}/bin" 2>/dev/null
    chmod 0750 "${PRIVSTACK_DIR}/bin" 2>/dev/null
    for f in "${PRIVSTACK_DIR}/bin"/*; do
        if [ -f "$f" ]; then
            chown 0:${PRIVSTACK_GID} "$f" 2>/dev/null
            chmod 0750 "$f" 2>/dev/null
        fi
    done
fi

# Config: 0600 root:root (sensitive)
if [ -d "${PRIVSTACK_DIR}/config" ]; then
    chown 0:0 "${PRIVSTACK_DIR}/config" 2>/dev/null
    chmod 0700 "${PRIVSTACK_DIR}/config" 2>/dev/null
    for f in "${PRIVSTACK_DIR}/config"/*; do
        if [ -f "$f" ]; then
            chown 0:0 "$f" 2>/dev/null
            chmod 0600 "$f" 2>/dev/null
        fi
    done
fi

# Scripts: 0755 root:root
if [ -d "${PRIVSTACK_DIR}/scripts" ]; then
    chown 0:0 "${PRIVSTACK_DIR}/scripts" 2>/dev/null
    chmod 0755 "${PRIVSTACK_DIR}/scripts" 2>/dev/null
    for f in "${PRIVSTACK_DIR}/scripts"/*; do
        if [ -f "$f" ]; then
            chown 0:0 "$f" 2>/dev/null
            chmod 0755 "$f" 2>/dev/null
        fi
    done
fi

# Run: 0750 root:23333
chown 0:${PRIVSTACK_GID} "${PRIVSTACK_DIR}/run" 2>/dev/null
chmod 0750 "${PRIVSTACK_DIR}/run" 2>/dev/null

# Logs: 0700 root:root — may contain proxy endpoints and diagnostics.
chown 0:0 "${PRIVSTACK_DIR}/logs" 2>/dev/null
chmod 0700 "${PRIVSTACK_DIR}/logs" 2>/dev/null
for f in "${PRIVSTACK_DIR}/logs"/*; do
    [ -f "$f" ] && chmod 0600 "$f" 2>/dev/null
done

log_info "Permissions set"

# ============================================================================
# 3. Enable IP forwarding (IPv4 and IPv6)
# ============================================================================

# IPv4 forwarding — required for tproxy to route packets through the proxy
if [ -f /proc/sys/net/ipv4/ip_forward ]; then
    echo 1 > /proc/sys/net/ipv4/ip_forward 2>/dev/null
    log_info "IPv4 ip_forward enabled"
fi

# IPv6 forwarding — required if ipv6.mode != disabled
if [ -f /proc/sys/net/ipv6/conf/all/forwarding ]; then
    echo 1 > /proc/sys/net/ipv6/conf/all/forwarding 2>/dev/null
    log_info "IPv6 forwarding enabled"
fi

# ============================================================================
# 4. Disable reverse path filtering (rp_filter)
# ============================================================================

# TPROXY requires rp_filter=0 because the proxy returns packets that
# did not originate from this host. With rp_filter=1 the kernel drops
# these packets as "martians".

for rp_path in /proc/sys/net/ipv4/conf/all/rp_filter \
               /proc/sys/net/ipv4/conf/default/rp_filter; do
    if [ -f "$rp_path" ]; then
        echo 0 > "$rp_path" 2>/dev/null
    fi
done

# Also disable on per-interface basis for common interfaces
for iface in wlan0 rmnet0 rmnet_data0 ccmni0 v4-wlan0; do
    rp_iface="/proc/sys/net/ipv4/conf/${iface}/rp_filter"
    if [ -f "$rp_iface" ]; then
        echo 0 > "$rp_iface" 2>/dev/null
    fi
done

log_info "rp_filter disabled for TPROXY"

# ============================================================================
# 5. Verify critical binaries exist
# ============================================================================

MISSING_BIN=0

for bin_name in sing-box privd; do
    bin_path="${PRIVSTACK_DIR}/bin/${bin_name}"
    if [ ! -f "$bin_path" ]; then
        log_warn "Binary missing: ${bin_path}"
        MISSING_BIN=1
    elif [ ! -x "$bin_path" ]; then
        # Try to fix executable bit
        chmod 0750 "$bin_path" 2>/dev/null
        if [ ! -x "$bin_path" ]; then
            log_warn "Binary not executable: ${bin_path}"
            MISSING_BIN=1
        fi
    fi
done

if [ "$MISSING_BIN" -eq 1 ]; then
    log_warn "Some binaries are missing — daemon may fail to start"
else
    log_info "All required binaries verified"
fi

# ============================================================================
# 6. Clean stale runtime files from previous boot
# ============================================================================

# Remove stale PID file and socket — service.sh will create fresh ones
rm -f "${PRIVSTACK_DIR}/run/privd.pid" 2>/dev/null
rm -f "${PRIVSTACK_DIR}/run/daemon.sock" 2>/dev/null
rm -f "${PRIVSTACK_DIR}/run/singbox.pid" 2>/dev/null
rm -f "${PRIVSTACK_DIR}/run/active" 2>/dev/null
rm -f "${PRIVSTACK_DIR}/run/reset.lock" 2>/dev/null

log_info "Cleaned stale runtime files"

log_info "post-fs-data initialization complete"
