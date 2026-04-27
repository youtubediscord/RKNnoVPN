#!/usr/bin/env bash
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/common.sh"

ALLOW_RESET=0
ALLOW_START=0
STOP_AFTER_START=0

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
        --allow-reset)
            ALLOW_RESET=1
            ;;
        --allow-start)
            ALLOW_START=1
            ;;
        --stop-after-start)
            STOP_AFTER_START=1
            ;;
        -h|--help)
            sed -n '1,160p' "$SCRIPT_DIR/README.md"
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
ensure_privctl
RUN_DIR="$(make_run_dir)"
FAILED=0

note "running safe PrivStack smoke checks into $RUN_DIR"

capture_su privctl_version "'$PRIVCTL_PATH' version" || FAILED=1
capture_su status_before "'$PRIVCTL_PATH' status" || FAILED=1
capture_su self_check_before "'$PRIVCTL_PATH' self-check" || FAILED=1
capture_su_raw doctor_before.json "'$PRIVCTL_PATH' doctor '{\"lines\":160}'" || FAILED=1

if command -v python3 >/dev/null 2>&1; then
	python3 "$SCRIPT_DIR/check_doctor.py" "$RUN_DIR/doctor_before.json" --strict-package-resolution >"$RUN_DIR/doctor_before_check.txt" 2>&1 || FAILED=1
else
	printf '%s\n' "python3 not found; skipped doctor JSON validation" >"$RUN_DIR/doctor_before_check.txt"
fi

if [ "$ALLOW_RESET" = "1" ]; then
	note "running explicit reset smoke step"
	capture_su network_reset "'$PRIVCTL_PATH' backend.reset" || FAILED=1
	capture_su_raw doctor_after_reset.json "'$PRIVCTL_PATH' doctor '{\"lines\":160}'" || FAILED=1
	if command -v python3 >/dev/null 2>&1; then
		python3 "$SCRIPT_DIR/check_doctor.py" "$RUN_DIR/doctor_after_reset.json" --strict-package-resolution >"$RUN_DIR/doctor_after_reset_check.txt" 2>&1 || FAILED=1
	fi
fi

if [ "$ALLOW_START" = "1" ]; then
	note "running explicit start smoke step"
	capture_su start "'$PRIVCTL_PATH' start" || FAILED=1
	capture_su_raw doctor_after_start.json "'$PRIVCTL_PATH' doctor '{\"lines\":160}'" || FAILED=1
	if command -v python3 >/dev/null 2>&1; then
		python3 "$SCRIPT_DIR/check_doctor.py" "$RUN_DIR/doctor_after_start.json" --strict-package-resolution >"$RUN_DIR/doctor_after_start_check.txt" 2>&1 || FAILED=1
	fi
	if [ "$STOP_AFTER_START" = "1" ]; then
		note "stopping after explicit start smoke step"
		capture_su stop_after_start "'$PRIVCTL_PATH' stop" || FAILED=1
		capture_su_raw doctor_after_stop.json "'$PRIVCTL_PATH' doctor '{\"lines\":160}'" || FAILED=1
	fi
fi

note "done: $RUN_DIR"
if [ "$FAILED" != "0" ]; then
	die "one or more smoke checks failed; inspect $RUN_DIR"
fi
