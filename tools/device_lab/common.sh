#!/usr/bin/env bash
set -u

ADB="${ADB:-adb}"
ADB_SERIAL="${ADB_SERIAL:-}"
RKNNOVPN_DIR="${RKNNOVPN_DIR:-/data/adb/modules/rknnovpn}"
DAEMONCTL_PATH="${DAEMONCTL_PATH:-$RKNNOVPN_DIR/bin/daemonctl}"
OUT_ROOT="${OUT_ROOT:-lab-artifacts/device_lab}"

die() {
    printf 'ERROR: %s\n' "$*" >&2
    exit 1
}

note() {
    printf '==> %s\n' "$*"
}

adb_base() {
    if [ -n "$ADB_SERIAL" ]; then
        "$ADB" -s "$ADB_SERIAL" "$@"
    else
        "$ADB" "$@"
    fi
}

adb_shell() {
    adb_base shell "$@"
}

adb_su() {
    adb_shell su -c "$1"
}

ensure_adb() {
    command -v "$ADB" >/dev/null 2>&1 || die "adb not found; set ADB=/path/to/adb"
}

ensure_single_device() {
    ensure_adb
    if [ -n "$ADB_SERIAL" ]; then
        adb_base get-state >/dev/null 2>&1 || die "device $ADB_SERIAL is not available"
        return
    fi

    devices="$("$ADB" devices | awk 'NR > 1 && $2 == "device" {print $1}')"
    count="$(printf '%s\n' "$devices" | sed '/^$/d' | wc -l | tr -d ' ')"
    if [ "$count" != "1" ]; then
        printf '%s\n' "$devices" >&2
        die "expected exactly one adb device; set ADB_SERIAL when several are connected"
    fi
    ADB_SERIAL="$devices"
}

ensure_root_shell() {
    adb_su "id" >/dev/null 2>&1 || die "su is not available through adb shell"
}

ensure_daemonctl() {
    adb_su "test -x '$DAEMONCTL_PATH'" >/dev/null 2>&1 || die "daemonctl is missing or not executable at $DAEMONCTL_PATH"
}

make_run_dir() {
    mkdir -p "$OUT_ROOT"
    stamp="$(date -u +%Y%m%dT%H%M%SZ)"
    serial_safe="$(printf '%s' "$ADB_SERIAL" | tr -c 'A-Za-z0-9_.-' '_')"
    RUN_DIR="$OUT_ROOT/${stamp}_${serial_safe}"
    mkdir -p "$RUN_DIR"
    printf '%s\n' "$RUN_DIR"
}

capture_host() {
    name="$1"
    shift
    out="$RUN_DIR/$name.txt"
    {
        printf '$'
        printf ' %s' "$@"
        printf '\n\n'
        "$@"
        code="$?"
        printf '\n[exit=%s]\n' "$code"
        return "$code"
    } >"$out" 2>&1
}

capture_shell() {
    name="$1"
    command="$2"
    out="$RUN_DIR/$name.txt"
    {
        printf '$ adb shell %s\n\n' "$command"
        adb_shell sh -c "$command"
        code="$?"
        printf '\n[exit=%s]\n' "$code"
        return "$code"
    } >"$out" 2>&1
}

capture_su() {
    name="$1"
    command="$2"
    out="$RUN_DIR/$name.txt"
    {
        printf '$ adb shell su -c %s\n\n' "$command"
        adb_su "$command"
        code="$?"
        printf '\n[exit=%s]\n' "$code"
        return "$code"
    } >"$out" 2>&1
}

capture_su_raw() {
    name="$1"
    command="$2"
    out="$RUN_DIR/$name"
    adb_su "$command" >"$out" 2>"$out.stderr"
    code="$?"
    printf '%s\n' "$code" >"$out.exit"
    return "$code"
}
