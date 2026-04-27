#!/system/bin/sh
# Shared RKNnoVPN module runtime helpers.
# POSIX sh compatible; safe to source from boot/install/rescue scripts.

RKNNOVPN_DIR="${RKNNOVPN_DIR:-${MODDIR:-${MODPATH:-/data/adb/modules/rknnovpn}}}"
RKNNOVPN_GID="${RKNNOVPN_GID:-23333}"

BIN_DIR="${BIN_DIR:-${RKNNOVPN_DIR}/bin}"
CONFIG_DIR="${CONFIG_DIR:-${RKNNOVPN_DIR}/config}"
RENDERED_CONFIG_DIR="${RENDERED_CONFIG_DIR:-${CONFIG_DIR}/rendered}"
SCRIPTS_DIR="${SCRIPTS_DIR:-${RKNNOVPN_DIR}/scripts}"
RUN_DIR="${RUN_DIR:-${RKNNOVPN_DIR}/run}"
DATA_DIR="${DATA_DIR:-${RKNNOVPN_DIR}/data}"
LOG_DIR="${LOG_DIR:-${RKNNOVPN_DIR}/logs}"
BACKUP_DIR="${BACKUP_DIR:-${RKNNOVPN_DIR}/backup}"
PROFILES_DIR="${PROFILES_DIR:-${RKNNOVPN_DIR}/profiles}"
RELEASES_DIR="${RELEASES_DIR:-${RKNNOVPN_DIR}/releases}"

RESET_LOCK="${RESET_LOCK:-${RUN_DIR}/reset.lock}"
ACTIVE_FILE="${ACTIVE_FILE:-${RUN_DIR}/active}"
MANUAL_FLAG="${MANUAL_FLAG:-${CONFIG_DIR}/manual}"
DAEMON_PID_FILE="${DAEMON_PID_FILE:-${RUN_DIR}/daemon.pid}"
SINGBOX_PID_FILE="${SINGBOX_PID_FILE:-${RUN_DIR}/singbox.pid}"
DAEMON_SOCK="${DAEMON_SOCK:-${RUN_DIR}/daemon.sock}"

FWMARK="${FWMARK:-0x2023}"
ROUTE_TABLE="${ROUTE_TABLE:-2023}"
ROUTE_TABLE_V6="${ROUTE_TABLE_V6:-2024}"
IPT_WAIT="${IPT_WAIT:--w 100}"

rknnovpn_log() {
    _level="$1"
    shift
    _tag="${RKNNOVPN_LOG_TAG:-${TAG:-rknnovpn}}"
    _msg="$*"
    case "$_level" in
        e|E|error|ERROR) _prio="e"; _prefix="ERROR" ;;
        w|W|warn|WARN) _prio="w"; _prefix="WARN" ;;
        *) _prio="i"; _prefix="INFO" ;;
    esac
    if [ -n "${RKNNOVPN_LOG_FILE:-}" ]; then
        _ts="$(date '+%Y-%m-%d %H:%M:%S' 2>/dev/null || echo '----')"
        echo "$_ts [$_prefix] $_msg" >> "$RKNNOVPN_LOG_FILE" 2>/dev/null || true
    fi
    /system/bin/log -t "$_tag" -p "$_prio" "$_msg" 2>/dev/null || true
    if [ "$_prio" = "e" ] || [ "$_prio" = "w" ]; then
        echo "[$_tag] $_prefix: $_msg" >&2
    else
        echo "[$_tag] $_prefix: $_msg"
    fi
}

rknnovpn_log_info() { rknnovpn_log i "$@"; }
rknnovpn_log_warn() { rknnovpn_log w "$@"; }
rknnovpn_log_error() { rknnovpn_log e "$@"; }

rknnovpn_ensure_layout() {
    for _dir in "$BIN_DIR" "$CONFIG_DIR" "$RENDERED_CONFIG_DIR" "$SCRIPTS_DIR" "$RUN_DIR" "$DATA_DIR" "$LOG_DIR" "$BACKUP_DIR" "$PROFILES_DIR" "$RELEASES_DIR"; do
        mkdir -p "$_dir" 2>/dev/null || return 1
    done
}

rknnovpn_apply_data_permissions() {
    chown 0:0 "$RKNNOVPN_DIR" 2>/dev/null || true
    chmod 0755 "$RKNNOVPN_DIR" 2>/dev/null || true

    chown -R 0:"$RKNNOVPN_GID" "$BIN_DIR" 2>/dev/null || true
    chmod 0750 "$BIN_DIR" 2>/dev/null || true
    find "$BIN_DIR" -type f -exec chmod 0750 {} \; 2>/dev/null || true

    chown -R 0:0 "$SCRIPTS_DIR" 2>/dev/null || true
    chmod 0755 "$SCRIPTS_DIR" 2>/dev/null || true
    find "$SCRIPTS_DIR" -type d -exec chmod 0755 {} \; 2>/dev/null || true
    find "$SCRIPTS_DIR" -type f -exec chmod 0755 {} \; 2>/dev/null || true

    chown -R 0:0 "$CONFIG_DIR" 2>/dev/null || true
    chmod 0700 "$CONFIG_DIR" 2>/dev/null || true
    find "$CONFIG_DIR" -type f -exec chmod 0600 {} \; 2>/dev/null || true
    chmod 0700 "$RENDERED_CONFIG_DIR" 2>/dev/null || true

    chown -R 0:"$RKNNOVPN_GID" "$RUN_DIR" 2>/dev/null || true
    chmod 0750 "$RUN_DIR" 2>/dev/null || true

    chown -R 0:0 "$DATA_DIR" "$LOG_DIR" "$BACKUP_DIR" "$PROFILES_DIR" "$RELEASES_DIR" 2>/dev/null || true
    chmod 0700 "$DATA_DIR" "$LOG_DIR" "$BACKUP_DIR" "$PROFILES_DIR" "$RELEASES_DIR" 2>/dev/null || true
    find "$LOG_DIR" -type f -exec chmod 0600 {} \; 2>/dev/null || true
}

rknnovpn_enter_reset_mode() {
    mkdir -p "$RUN_DIR" 2>/dev/null || true
    touch "$RESET_LOCK" 2>/dev/null || true
    rm -f "$ACTIVE_FILE" 2>/dev/null || true
}

rknnovpn_leave_reset_mode() {
    rm -f "$RESET_LOCK" 2>/dev/null || true
}

rknnovpn_is_reset_active() {
    [ -f "$RESET_LOCK" ]
}

rknnovpn_set_manual_mode() {
    mkdir -p "$CONFIG_DIR" "$RUN_DIR" 2>/dev/null || true
    touch "$MANUAL_FLAG" 2>/dev/null || true
    rm -f "$ACTIVE_FILE" 2>/dev/null || true
}

rknnovpn_clear_manual_mode() {
    rm -f "$MANUAL_FLAG" 2>/dev/null || true
}

rknnovpn_is_manual_mode() {
    [ -f "$MANUAL_FLAG" ]
}

rknnovpn_mark_active() {
    mkdir -p "$RUN_DIR" 2>/dev/null || true
    touch "$ACTIVE_FILE" 2>/dev/null || true
}

rknnovpn_clear_active() {
    rm -f "$ACTIVE_FILE" 2>/dev/null || true
}

rknnovpn_is_active() {
    [ -f "$ACTIVE_FILE" ]
}

rknnovpn_has_boot_cleanup_markers() {
    for _marker in \
        "$ACTIVE_FILE" \
        "$RESET_LOCK" \
        "$DAEMON_PID_FILE" \
        "$SINGBOX_PID_FILE" \
        "$DAEMON_SOCK" \
        "$RUN_DIR/env.sh" \
        "$RUN_DIR/iptables.rules" \
        "$RUN_DIR/ip6tables.rules" \
        "$RUN_DIR/iptables_backup.rules" \
        "$RUN_DIR/ip6tables_backup.rules"; do
        [ -e "$_marker" ] && return 0
    done
    return 1
}

rknnovpn_remove_runtime_snapshots() {
    rm -f "$SINGBOX_PID_FILE" 2>/dev/null || true
    rm -f "$RUN_DIR/net_change.lock" 2>/dev/null || true
    rm -f "$RUN_DIR/env.sh" 2>/dev/null || true
    rm -f "$RUN_DIR/iptables.rules" "$RUN_DIR/ip6tables.rules" 2>/dev/null || true
    rm -f "$RUN_DIR/iptables_backup.rules" "$RUN_DIR/ip6tables_backup.rules" 2>/dev/null || true
}

rknnovpn_remove_daemon_runtime_files() {
    rm -f "$DAEMON_PID_FILE" "$DAEMON_SOCK" 2>/dev/null || true
}
