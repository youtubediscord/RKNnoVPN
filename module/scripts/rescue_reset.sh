#!/system/bin/sh
# ============================================================================
# RKNnoVPN rescue_reset.sh
#
# Root-level cleanup that does not trust daemon state, PID files, or an old
# iptables backup. It removes only RKNnoVPN-owned network artifacts.
#
# Modes:
#   daemon-reset    - called by daemon; keep daemon socket/process alive
#   --boot-clean    - called from service.sh before daemon starts
#   hard-reset      - external rescue; also kill daemon and remove daemon socket
#   update-clean    - called by updater; keep daemon socket/process alive
#   uninstall-clean - called by module removal; kill runtime without changing
#                     user config/manual state
# ============================================================================

set +e

MODE="${1:-hard-reset}"
case "$MODE" in
    reset)
        MODE="hard-reset"
        ;;
    --boot-clean|boot-clean)
        MODE="boot-clean"
        ;;
    daemon-reset|hard-reset|boot-clean|update-clean|uninstall-clean)
        ;;
    *)
        echo "Usage: $0 {daemon-reset|hard-reset|--boot-clean|update-clean|uninstall-clean}" >&2
        exit 2
        ;;
esac

IPT_WAIT_WAS_SET="${IPT_WAIT+x}"
SCRIPT_DIR="${0%/*}"
if [ -f "${SCRIPT_DIR}/lib/rknnovpn_env.sh" ]; then
    . "${SCRIPT_DIR}/lib/rknnovpn_env.sh"
fi
if [ -f "${SCRIPT_DIR}/lib/rknnovpn_netstack.sh" ]; then
    . "${SCRIPT_DIR}/lib/rknnovpn_netstack.sh"
fi
if [ -z "$IPT_WAIT_WAS_SET" ]; then
    if [ "$MODE" = "boot-clean" ]; then
        IPT_WAIT="-w 3"
    else
        IPT_WAIT="-w 10"
    fi
fi

LOG_FILE="$RKNNOVPN_DIR/logs/rescue_reset.log"
TAG="rknnovpn:reset"

mkdir -p "$RUN_DIR" "$CONFIG_DIR" "$RKNNOVPN_DIR/logs" 2>/dev/null

log() {
    _ts="$(date '+%Y-%m-%d %H:%M:%S' 2>/dev/null || echo '----')"
    _msg="$_ts [$MODE] $*"
    echo "$_msg" >> "$LOG_FILE" 2>/dev/null
    /system/bin/log -t "$TAG" -p i "$*" 2>/dev/null
    echo "$*"
}

rknnovpn_enter_reset_mode

if [ "$MODE" = "daemon-reset" ] || [ "$MODE" = "hard-reset" ]; then
    rknnovpn_set_manual_mode
fi

log "[1/8] asking daemon for graceful stop"
if [ -x "$RKNNOVPN_DIR/bin/daemonctl" ] && [ "$MODE" = "hard-reset" ]; then
    "$RKNNOVPN_DIR/bin/daemonctl" stop >/dev/null 2>&1 || true
fi

kill_matching_processes() {
    signal="$1"
    for p in /proc/[0-9]*; do
        pid="${p##*/}"
        [ "$pid" = "$$" ] && continue
        cmd="$(tr '\000' ' ' < "$p/cmdline" 2>/dev/null)"
        [ -z "$cmd" ] && continue

        case "$cmd" in
            *"$RKNNOVPN_DIR/bin/sing-box"*|\
            *"$RKNNOVPN_DIR/scripts/net_handler.sh"*|*"inotifyd"*"${RKNNOVPN_DIR}/scripts/net_handler.sh"*)
                log "$signal $pid :: $cmd"
                kill "-$signal" "$pid" 2>/dev/null
                ;;
            *"$RKNNOVPN_DIR/bin/daemon"*)
                if [ "$MODE" = "hard-reset" ] || [ "$MODE" = "boot-clean" ] || [ "$MODE" = "uninstall-clean" ]; then
                    log "$signal $pid :: $cmd"
                    kill "-$signal" "$pid" 2>/dev/null
                fi
                ;;
        esac
    done
}

log "[2/8] killing orphan daemon/sing-box/net_handler processes"
kill_matching_processes TERM
sleep 1
kill_matching_processes KILL

log "[3/8] stopping bundled runtime pieces"
if [ -x "$RKNNOVPN_DIR/scripts/dns.sh" ]; then
    IPT_WAIT="$IPT_WAIT" "$RKNNOVPN_DIR/scripts/dns.sh" stop >/dev/null 2>&1 || true
fi
if [ -x "$RKNNOVPN_DIR/scripts/iptables.sh" ]; then
    IPT_WAIT="$IPT_WAIT" "$RKNNOVPN_DIR/scripts/iptables.sh" stop >/dev/null 2>&1 || true
fi

log "[4/8] deleting RKNnoVPN iptables/ip6tables artifacts"
rknnovpn_cleanup_iptables_all

log "[5/8] deleting RKNnoVPN policy routing"
rknnovpn_delete_policy_routes

log "[6/8] removing stale runtime files"
rknnovpn_remove_runtime_snapshots
if [ "$MODE" = "hard-reset" ] || [ "$MODE" = "boot-clean" ] || [ "$MODE" = "uninstall-clean" ]; then
    rknnovpn_remove_daemon_runtime_files
fi

leftovers=""
add_leftover() {
    if [ -z "$leftovers" ]; then
        leftovers="$1"
    else
        leftovers="${leftovers}; $1"
    fi
}

log "[7/8] verifying cleanup"
out="$(rknnovpn_collect_netstack_leftovers)"
[ -n "$out" ] && add_leftover "$out"

log "[8/8] finishing"
if [ "$MODE" != "daemon-reset" ]; then
    rknnovpn_leave_reset_mode
fi

if [ -n "$leftovers" ]; then
    log "leftovers remain: $leftovers"
    exit 1
fi

log "cleanup complete"
exit 0
