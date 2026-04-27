#!/system/bin/sh
# ============================================================================
# PrivStack rescue_reset.sh
#
# Root-level cleanup that does not trust daemon state, PID files, or an old
# iptables backup. It removes only PrivStack-owned network artifacts.
#
# Modes:
#   daemon-reset    - called by privd; keep privd socket/process alive
#   --boot-clean    - called from service.sh before privd starts
#   hard-reset      - external rescue; also kill privd and remove daemon socket
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
    daemon-reset|hard-reset|boot-clean|uninstall-clean)
        ;;
    *)
        echo "Usage: $0 {daemon-reset|hard-reset|--boot-clean|uninstall-clean}" >&2
        exit 2
        ;;
esac

IPT_WAIT_WAS_SET="${IPT_WAIT+x}"
SCRIPT_DIR="${0%/*}"
if [ -f "${SCRIPT_DIR}/lib/privstack_env.sh" ]; then
    . "${SCRIPT_DIR}/lib/privstack_env.sh"
fi
if [ -f "${SCRIPT_DIR}/lib/privstack_netstack.sh" ]; then
    . "${SCRIPT_DIR}/lib/privstack_netstack.sh"
fi
if [ -z "$IPT_WAIT_WAS_SET" ]; then
    if [ "$MODE" = "boot-clean" ]; then
        IPT_WAIT="-w 3"
    else
        IPT_WAIT="-w 10"
    fi
fi

LOG_FILE="$PRIVSTACK_DIR/logs/rescue_reset.log"
TAG="privstack:reset"

mkdir -p "$RUN_DIR" "$CONFIG_DIR" "$PRIVSTACK_DIR/logs" 2>/dev/null

log() {
    _ts="$(date '+%Y-%m-%d %H:%M:%S' 2>/dev/null || echo '----')"
    _msg="$_ts [$MODE] $*"
    echo "$_msg" >> "$LOG_FILE" 2>/dev/null
    /system/bin/log -t "$TAG" -p i "$*" 2>/dev/null
    echo "$*"
}

privstack_enter_reset_mode

if [ "$MODE" = "daemon-reset" ] || [ "$MODE" = "hard-reset" ]; then
    privstack_set_manual_mode
fi

log "[1/8] asking daemon for graceful stop"
if [ -x "$PRIVSTACK_DIR/bin/privctl" ] && [ "$MODE" = "hard-reset" ]; then
    "$PRIVSTACK_DIR/bin/privctl" stop >/dev/null 2>&1 || true
fi

kill_matching_processes() {
    signal="$1"
    for p in /proc/[0-9]*; do
        pid="${p##*/}"
        [ "$pid" = "$$" ] && continue
        cmd="$(tr '\000' ' ' < "$p/cmdline" 2>/dev/null)"
        [ -z "$cmd" ] && continue

        case "$cmd" in
            *"$PRIVSTACK_DIR/bin/sing-box"*|*" sing-box "*|\
            *"net_handler.sh"*|*"inotifyd"*"net_handler.sh"*)
                log "$signal $pid :: $cmd"
                kill "-$signal" "$pid" 2>/dev/null
                ;;
            *"$PRIVSTACK_DIR/bin/privd"*|*" privd "*)
                if [ "$MODE" = "hard-reset" ] || [ "$MODE" = "boot-clean" ] || [ "$MODE" = "uninstall-clean" ]; then
                    log "$signal $pid :: $cmd"
                    kill "-$signal" "$pid" 2>/dev/null
                fi
                ;;
        esac
    done
}

log "[2/8] killing orphan privd/sing-box/net_handler processes"
kill_matching_processes TERM
sleep 1
kill_matching_processes KILL

log "[3/8] stopping bundled runtime pieces"
if [ -x "$PRIVSTACK_DIR/scripts/dns.sh" ]; then
    IPT_WAIT="$IPT_WAIT" "$PRIVSTACK_DIR/scripts/dns.sh" stop >/dev/null 2>&1 || true
fi
if [ -x "$PRIVSTACK_DIR/scripts/iptables.sh" ]; then
    IPT_WAIT="$IPT_WAIT" "$PRIVSTACK_DIR/scripts/iptables.sh" stop >/dev/null 2>&1 || true
fi

log "[4/8] deleting PrivStack iptables/ip6tables artifacts"
privstack_cleanup_iptables_all

log "[5/8] deleting PrivStack policy routing"
privstack_delete_policy_routes

log "[6/8] removing stale runtime files"
privstack_remove_runtime_snapshots
if [ "$MODE" = "hard-reset" ] || [ "$MODE" = "boot-clean" ] || [ "$MODE" = "uninstall-clean" ]; then
    privstack_remove_daemon_runtime_files
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
out="$(privstack_collect_netstack_leftovers)"
[ -n "$out" ] && add_leftover "$out"

log "[8/8] finishing"
if [ "$MODE" != "daemon-reset" ]; then
    privstack_leave_reset_mode
fi

if [ -n "$leftovers" ]; then
    log "leftovers remain: $leftovers"
    exit 1
fi

log "cleanup complete"
exit 0
