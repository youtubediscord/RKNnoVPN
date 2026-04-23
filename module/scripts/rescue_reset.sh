#!/system/bin/sh
# ============================================================================
# PrivStack rescue_reset.sh
#
# Root-level cleanup that does not trust daemon state, PID files, or an old
# iptables backup. It removes only PrivStack-owned network artifacts.
#
# Modes:
#   daemon-reset  - called by privd; keep privd socket/process alive
#   --boot-clean  - called from service.sh before privd starts
#   hard-reset    - external rescue; also kill privd and remove daemon socket
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
    daemon-reset|hard-reset|boot-clean)
        ;;
    *)
        echo "Usage: $0 {daemon-reset|hard-reset|--boot-clean}" >&2
        exit 2
        ;;
esac

PRIVSTACK_DIR="${PRIVSTACK_DIR:-/data/adb/privstack}"
FWMARK="${FWMARK:-0x2023}"
ROUTE_TABLE="${ROUTE_TABLE:-2023}"
ROUTE_TABLE_V6="${ROUTE_TABLE_V6:-2024}"
IPT_WAIT="-w 20"

RUN_DIR="$PRIVSTACK_DIR/run"
CONFIG_DIR="$PRIVSTACK_DIR/config"
RESET_LOCK="$RUN_DIR/reset.lock"
ACTIVE_FILE="$RUN_DIR/active"
MANUAL_FLAG="$CONFIG_DIR/manual"
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

touch "$RESET_LOCK" 2>/dev/null
rm -f "$ACTIVE_FILE" 2>/dev/null

if [ "$MODE" != "boot-clean" ]; then
    touch "$MANUAL_FLAG" 2>/dev/null
fi

log "[1/7] stopping bundled runtime pieces"
if [ -x "$PRIVSTACK_DIR/bin/privctl" ] && [ "$MODE" = "hard-reset" ]; then
    "$PRIVSTACK_DIR/bin/privctl" stop >/dev/null 2>&1 || true
fi
if [ -x "$PRIVSTACK_DIR/scripts/dns.sh" ]; then
    "$PRIVSTACK_DIR/scripts/dns.sh" stop >/dev/null 2>&1 || true
fi
if [ -x "$PRIVSTACK_DIR/scripts/iptables.sh" ]; then
    "$PRIVSTACK_DIR/scripts/iptables.sh" stop >/dev/null 2>&1 || true
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
                if [ "$MODE" = "hard-reset" ] || [ "$MODE" = "boot-clean" ]; then
                    log "$signal $pid :: $cmd"
                    kill "-$signal" "$pid" 2>/dev/null
                fi
                ;;
        esac
    done
}

log "[2/7] killing orphan privd/sing-box/net_handler processes"
kill_matching_processes TERM
sleep 1
kill_matching_processes KILL

cleanup_ipt() {
    ipt="$1"
    command -v "$ipt" >/dev/null 2>&1 || return 0

    for table in raw mangle nat filter; do
        $ipt $IPT_WAIT -t "$table" -S >/dev/null 2>&1 || continue
        log "cleaning $ipt table $table"

        n=0
        while [ "$n" -lt 200 ]; do
            rule="$($ipt $IPT_WAIT -t "$table" -S 2>/dev/null | grep -E -- ' (-j|-g) PRIVSTACK' | head -n 1)"
            [ -z "$rule" ] && break
            del="$(printf '%s\n' "$rule" | sed 's/^-A /-D /')"
            $ipt $IPT_WAIT -t "$table" $del 2>/dev/null || break
            n=$((n + 1))
        done

        n=0
        while [ "$n" -lt 200 ]; do
            ch="$($ipt $IPT_WAIT -t "$table" -S 2>/dev/null | awk '/^-N PRIVSTACK/ {print $2; exit}')"
            [ -z "$ch" ] && break
            $ipt $IPT_WAIT -t "$table" -F "$ch" 2>/dev/null || true
            $ipt $IPT_WAIT -t "$table" -X "$ch" 2>/dev/null || true
            n=$((n + 1))
        done
    done
}

log "[3/7] deleting PrivStack iptables/ip6tables artifacts"
cleanup_ipt iptables
cleanup_ipt ip6tables
cleanup_ipt iptables-legacy
cleanup_ipt ip6tables-legacy
cleanup_ipt iptables-nft
cleanup_ipt ip6tables-nft

delete_rules() {
    i=0
    while [ "$i" -lt 100 ]; do
        ip rule del fwmark "$FWMARK" table "$ROUTE_TABLE" 2>/dev/null && { i=$((i + 1)); continue; }
        ip rule del fwmark "$FWMARK" 2>/dev/null && { i=$((i + 1)); continue; }
        ip rule del table "$ROUTE_TABLE" 2>/dev/null && { i=$((i + 1)); continue; }
        break
    done

    i=0
    while [ "$i" -lt 100 ]; do
        ip -6 rule del fwmark "$FWMARK" table "$ROUTE_TABLE_V6" 2>/dev/null && { i=$((i + 1)); continue; }
        ip -6 rule del fwmark "$FWMARK" 2>/dev/null && { i=$((i + 1)); continue; }
        ip -6 rule del table "$ROUTE_TABLE_V6" 2>/dev/null && { i=$((i + 1)); continue; }
        break
    done
}

log "[4/7] deleting PrivStack policy routing"
delete_rules
ip route del local default dev lo table "$ROUTE_TABLE" 2>/dev/null || true
ip route flush table "$ROUTE_TABLE" 2>/dev/null || true
ip -6 route del local default dev lo table "$ROUTE_TABLE_V6" 2>/dev/null || true
ip -6 route flush table "$ROUTE_TABLE_V6" 2>/dev/null || true

log "[5/7] removing stale runtime files"
rm -f "$RUN_DIR/singbox.pid" 2>/dev/null
rm -f "$RUN_DIR/net_change.lock" 2>/dev/null
rm -f "$RUN_DIR/env.sh" 2>/dev/null
rm -f "$RUN_DIR/iptables.rules" "$RUN_DIR/ip6tables.rules" 2>/dev/null
rm -f "$RUN_DIR/iptables_backup.rules" "$RUN_DIR/ip6tables_backup.rules" 2>/dev/null
if [ "$MODE" = "hard-reset" ] || [ "$MODE" = "boot-clean" ]; then
    rm -f "$RUN_DIR/privd.pid" "$RUN_DIR/daemon.sock" 2>/dev/null
fi

leftovers=""
add_leftover() {
    if [ -z "$leftovers" ]; then
        leftovers="$1"
    else
        leftovers="${leftovers}; $1"
    fi
}

log "[6/7] verifying cleanup"
for ipt in iptables ip6tables; do
    command -v "$ipt" >/dev/null 2>&1 || continue
    for table in raw mangle nat filter; do
        out="$($ipt $IPT_WAIT -t "$table" -S 2>/dev/null | grep PRIVSTACK | head -n 1)"
        [ -n "$out" ] && add_leftover "$ipt/$table: $out"
    done
done

out="$(ip rule show 2>/dev/null | grep -E "$ROUTE_TABLE|$FWMARK" | head -n 1)"
[ -n "$out" ] && add_leftover "ip rule: $out"
out="$(ip -6 rule show 2>/dev/null | grep -E "$ROUTE_TABLE_V6|$FWMARK" | head -n 1)"
[ -n "$out" ] && add_leftover "ip -6 rule: $out"
out="$(ip route show table "$ROUTE_TABLE" 2>/dev/null | head -n 1)"
[ -n "$out" ] && add_leftover "ip route table $ROUTE_TABLE: $out"
out="$(ip -6 route show table "$ROUTE_TABLE_V6" 2>/dev/null | head -n 1)"
[ -n "$out" ] && add_leftover "ip -6 route table $ROUTE_TABLE_V6: $out"

log "[7/7] finishing"
if [ "$MODE" != "daemon-reset" ]; then
    rm -f "$RESET_LOCK" 2>/dev/null
fi

if [ -n "$leftovers" ]; then
    log "leftovers remain: $leftovers"
    exit 1
fi

log "cleanup complete"
exit 0
