#!/usr/bin/env bash
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/common.sh"

INCLUDE_PACKAGES=0

while [ "$#" -gt 0 ]; do
    case "$1" in
        --serial)
            shift
            [ "$#" -gt 0 ] || die "--serial requires a value"
            ADB_SERIAL="$1"
            ;;
        --out-root)
            shift
            [ "$#" -gt 0 ] || die "--out-root requires a value"
            OUT_ROOT="$1"
            ;;
        --include-packages)
            INCLUDE_PACKAGES=1
            ;;
        -h|--help)
            sed -n '1,120p' "$SCRIPT_DIR/README.md"
            exit 0
            ;;
        *)
            die "unknown argument: $1"
            ;;
    esac
    shift
done

ensure_single_device
ensure_root_shell
RUN_DIR="$(make_run_dir)"

note "collecting read-only diagnostics into $RUN_DIR"

capture_host adb_devices "$ADB" devices -l || true
capture_shell shell_id "id" || true
capture_su su_id "id" || true
capture_shell getprop_basic "getprop ro.product.manufacturer; getprop ro.product.model; getprop ro.build.version.release; getprop ro.build.version.sdk; getprop ro.product.cpu.abi" || true
capture_su root_stack_listing "ls -la /data/adb /data/adb/modules /data/adb/modules/rknnovpn '$RKNNOVPN_DIR' '$RKNNOVPN_DIR/bin' '$RKNNOVPN_DIR/run' '$RKNNOVPN_DIR/logs' 2>&1" || true
capture_su package_source_status "ls -l /data/system/packages.list 2>&1; cmd package list packages -U 2>&1 | wc -l; su -lp 2000 -c 'cmd package list packages -U' 2>&1 | wc -l" || true
capture_su network_rules "ip rule show; ip -6 rule show; ip route show table 2023; ip -6 route show table 2024" || true
capture_su iptables_mangle "iptables-save -t mangle; ip6tables-save -t mangle" || true
capture_su iptables_nat_raw_filter "iptables-save -t nat; iptables-save -t raw; iptables-save -t filter; ip6tables-save -t nat; ip6tables-save -t raw; ip6tables-save -t filter" || true
capture_su listeners "ss -lntu 2>&1 || netstat -lntu 2>&1" || true
capture_su connectivity "dumpsys connectivity 2>&1 | sed -n '1,220p'" || true
capture_su system_proxy "settings get global http_proxy; settings get global global_http_proxy_host; settings get global global_http_proxy_port" || true

if adb_su "test -x '$PRIVCTL_PATH'" >/dev/null 2>&1; then
    capture_su privctl_version "'$PRIVCTL_PATH' version" || true
    capture_su privctl_status "'$PRIVCTL_PATH' status" || true
    capture_su privctl_self_check "'$PRIVCTL_PATH' self-check" || true
    capture_su_raw doctor.json "'$PRIVCTL_PATH' doctor '{\"lines\":160}'" || true
    if command -v python3 >/dev/null 2>&1; then
        python3 "$SCRIPT_DIR/check_doctor.py" "$RUN_DIR/doctor.json" >"$RUN_DIR/doctor_check.txt" 2>&1 || true
    fi
else
    printf '%s\n' "privctl is not installed at $PRIVCTL_PATH" >"$RUN_DIR/privctl_missing.txt"
fi

if [ "$INCLUDE_PACKAGES" = "1" ]; then
    capture_su installed_packages "cmd package list packages -U" || true
fi

note "done: $RUN_DIR"
