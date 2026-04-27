#!/usr/bin/env bash
set -u

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
. "$SCRIPT_DIR/common.sh"

EMU_SERIAL="${ADB_SERIAL:-${EMU_SERIAL:-localhost:5555}}"
EMU_ADB_PORT="${EMU_ADB_PORT:-}"
EMU_FASTBOOT_PORT="${EMU_FASTBOOT_PORT:-5654}"
VM_DIR="${VM_DIR:-}"
RUN_VM="${RUN_VM:-run_vm.sh}"
APK_PATH="${APK_PATH:-}"
MODULE_ZIP="${MODULE_ZIP:-}"
INCLUDE_PACKAGES=0
ALLOW_RESET=0
ALLOW_START=0
STOP_AFTER_START=0
ALLOW_MODULE_INSTALL=0
REBOOT_AFTER_INSTALL=0
SKIP_CONNECT=0
BOOT_TIMEOUT_SEC="${BOOT_TIMEOUT_SEC:-600}"
NBD_DEVICE="${NBD_DEVICE:-/dev/nbd0}"

usage() {
    cat <<'EOF'
# Lineage/QEMU Device Lab

Usage:
  tools/device_lab/lineage_emulator.sh start --vm-dir PATH
  tools/device_lab/lineage_emulator.sh prepare-insecure-adb --vm-dir PATH
  tools/device_lab/lineage_emulator.sh connect
  tools/device_lab/lineage_emulator.sh doctor
  tools/device_lab/lineage_emulator.sh collect [--include-packages]
  tools/device_lab/lineage_emulator.sh smoke [--allow-reset] [--allow-start] [--stop-after-start]
  tools/device_lab/lineage_emulator.sh install --apk PATH --module PATH --allow-module-install --reboot-after-install

Defaults:
  --serial localhost:5555

Options:
  --adb PATH                  adb binary to use
  --serial SERIAL             adb serial, usually localhost:5555
  --adb-port PORT             host ADB forward port for start/connect
  --fastboot-port PORT        host fastboot forward port for start, default 5654
  --out-root PATH             artifact directory, default lab-artifacts/device_lab
  --vm-dir PATH               extracted android-lineage-emulator directory
  --run-vm NAME               VM launcher inside --vm-dir, default run_vm.sh
  --nbd-device PATH           nbd device for prepare-insecure-adb, default /dev/nbd0
  --apk PATH                  panel APK for install
  --module PATH               Magisk module ZIP for install
  --include-packages          include package list in collect output
  --allow-reset               allow smoke reset RPC
  --allow-start               allow smoke start RPC
  --stop-after-start          stop after smoke start RPC
  --allow-module-install      allow Magisk module installation
  --reboot-after-install      reboot after installing artifacts
  --skip-connect              do not run adb connect before the command
EOF
}

command_name="${1:-}"
if [ -n "$command_name" ]; then
    shift
fi

while [ "$#" -gt 0 ]; do
    case "$1" in
        --adb)
            shift
            [ "$#" -gt 0 ] || die "--adb requires a value"
            ADB="$1"
            ;;
        --serial)
            shift
            [ "$#" -gt 0 ] || die "--serial requires a value"
            EMU_SERIAL="$1"
            ;;
        --adb-port)
            shift
            [ "$#" -gt 0 ] || die "--adb-port requires a value"
            EMU_ADB_PORT="$1"
            ;;
        --fastboot-port)
            shift
            [ "$#" -gt 0 ] || die "--fastboot-port requires a value"
            EMU_FASTBOOT_PORT="$1"
            ;;
        --out-root)
            shift
            [ "$#" -gt 0 ] || die "--out-root requires a value"
            OUT_ROOT="$1"
            ;;
        --vm-dir)
            shift
            [ "$#" -gt 0 ] || die "--vm-dir requires a value"
            VM_DIR="$1"
            ;;
        --run-vm)
            shift
            [ "$#" -gt 0 ] || die "--run-vm requires a value"
            RUN_VM="$1"
            ;;
        --nbd-device)
            shift
            [ "$#" -gt 0 ] || die "--nbd-device requires a value"
            NBD_DEVICE="$1"
            ;;
        --apk)
            shift
            [ "$#" -gt 0 ] || die "--apk requires a value"
            APK_PATH="$1"
            ;;
        --module)
            shift
            [ "$#" -gt 0 ] || die "--module requires a value"
            MODULE_ZIP="$1"
            ;;
        --include-packages)
            INCLUDE_PACKAGES=1
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
        --allow-module-install)
            ALLOW_MODULE_INSTALL=1
            ;;
        --reboot-after-install)
            REBOOT_AFTER_INSTALL=1
            ;;
        --skip-connect)
            SKIP_CONNECT=1
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            die "unknown argument: $1"
            ;;
    esac
    shift
done

if [ -n "$EMU_ADB_PORT" ]; then
    EMU_SERIAL="localhost:$EMU_ADB_PORT"
else
    case "$EMU_SERIAL" in
        *:*) EMU_ADB_PORT="${EMU_SERIAL##*:}" ;;
        *) EMU_ADB_PORT="5555" ;;
    esac
fi

ADB_SERIAL="$EMU_SERIAL"

connect_emulator() {
    ensure_adb
    case "$ADB_SERIAL" in
        *:*)
            note "connecting adb to $ADB_SERIAL"
            "$ADB" connect "$ADB_SERIAL" >/dev/null
            ;;
    esac
    adb_base wait-for-device
}

adb_shell_probe() {
    adb_base shell true >/dev/null 2>&1
}

describe_adb_shell_failure() {
    {
        printf 'adb transport is online, but adb shell service failed.\n'
        printf 'This emulator image is not usable for PrivStack lab checks until adb shell works.\n'
        printf 'Last adb shell error:\n'
        adb_base shell true
    } 2>&1
}

ensure_adb_shell_service() {
    if adb_shell_probe; then
        return 0
    fi
    describe_adb_shell_failure >&2
    return 1
}

wait_boot_completed() {
    note "waiting for Android boot completion on $ADB_SERIAL"
    deadline=$(( $(date +%s) + BOOT_TIMEOUT_SEC ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        boot_output="$(adb_shell getprop sys.boot_completed 2>&1)"
        boot_code="$?"
        boot_completed="$(printf '%s' "$boot_output" | tr -d '\r')"
        if [ "$boot_code" = "0" ] && [ "$boot_completed" = "1" ]; then
            note "device booted"
            return 0
        fi
        case "$boot_output" in
            *"error: closed"*|*"closed"*)
                describe_adb_shell_failure >&2
                return 1
                ;;
        esac
        sleep 2
    done
    die "timed out waiting for sys.boot_completed=1 on $ADB_SERIAL"
}

prepare_insecure_adb() {
    [ -n "$VM_DIR" ] || die "prepare-insecure-adb requires --vm-dir PATH"
    image="$VM_DIR/vda.qcow2"
    [ -f "$image" ] || die "QEMU disk image not found: $image"
    command -v qemu-nbd >/dev/null 2>&1 || die "qemu-nbd not found"
    [ "$(id -u)" = "0" ] || die "prepare-insecure-adb must run as root because qemu-nbd and mount need privileges"

    mount_dir="/tmp/privstack_lineage_persist"
    backup_path="$VM_DIR/grubenv.before-insecure-adb.bak"

    cleanup_prepare() {
        umount "$mount_dir" >/dev/null 2>&1 || true
        qemu-nbd --disconnect "$NBD_DEVICE" >/dev/null 2>&1 || true
    }
    trap cleanup_prepare EXIT

    qemu-nbd --disconnect "$NBD_DEVICE" >/dev/null 2>&1 || true
    qemu-nbd --connect="$NBD_DEVICE" "$image"
    partprobe "$NBD_DEVICE" >/dev/null 2>&1 || true
    sleep 1
    mkdir -p "$mount_dir"
    mount "${NBD_DEVICE}p9" "$mount_dir"

    cp "$mount_dir/grubenv" "$backup_path"
    if grep -a -q 'android_insecure_adb=1' "$mount_dir/grubenv"; then
        note "android_insecure_adb is already enabled"
    else
        perl -0pi -e 's/android_insecure_adb=0/android_insecure_adb=1/' "$mount_dir/grubenv"
        sync
    fi
    strings "$mount_dir/grubenv" | grep 'android_insecure_adb=' || die "android_insecure_adb key not found in grubenv"
    note "backup saved to $backup_path"
}

start_vm() {
    [ -n "$VM_DIR" ] || die "start requires --vm-dir PATH"
    [ -d "$VM_DIR" ] || die "VM directory not found: $VM_DIR"
    [ -x "$VM_DIR/$RUN_VM" ] || die "VM launcher is missing or not executable: $VM_DIR/$RUN_VM"
    mkdir -p "$OUT_ROOT"
    log_path="$OUT_ROOT/lineage_emulator_vm.log"
    case "$log_path" in
        /*) log_abs="$log_path" ;;
        *) log_abs="$(pwd)/$log_path" ;;
    esac
    launcher="$RUN_VM"
    if [ "$EMU_ADB_PORT" != "5555" ] || [ "$EMU_FASTBOOT_PORT" != "5554" ]; then
        launcher=".privstack_$RUN_VM"
        sed \
            -e "s/hostfwd=tcp::5554-:5554/hostfwd=tcp::$EMU_FASTBOOT_PORT-:5554/g" \
            -e "s/hostfwd=tcp::5555-:5555/hostfwd=tcp::$EMU_ADB_PORT-:5555/g" \
            "$VM_DIR/$RUN_VM" >"$VM_DIR/$launcher"
        chmod +x "$VM_DIR/$launcher"
    fi
    note "starting $VM_DIR/$launcher in background; log: $log_abs"
    (
        cd "$VM_DIR" || exit 1
        nohup "./$launcher" >"$log_abs" 2>&1 &
    )
}

run_collect() {
    connect_emulator
    ensure_adb_shell_service || exit 1
    wait_boot_completed
    args=(--serial "$ADB_SERIAL" --out-root "$OUT_ROOT")
    if [ "$INCLUDE_PACKAGES" = "1" ]; then
        args+=(--include-packages)
    fi
    ADB="$ADB" "$SCRIPT_DIR/collect.sh" "${args[@]}"
}

run_smoke() {
    connect_emulator
    ensure_adb_shell_service || exit 1
    wait_boot_completed
    args=(--serial "$ADB_SERIAL" --out-root "$OUT_ROOT")
    if [ "$ALLOW_RESET" = "1" ]; then
        args+=(--allow-reset)
    fi
    if [ "$ALLOW_START" = "1" ]; then
        args+=(--allow-start)
    fi
    if [ "$STOP_AFTER_START" = "1" ]; then
        args+=(--stop-after-start)
    fi
    ADB="$ADB" "$SCRIPT_DIR/smoke.sh" "${args[@]}"
}

install_artifacts() {
    [ -n "$APK_PATH" ] || [ -n "$MODULE_ZIP" ] || die "install requires --apk and/or --module"
    if [ "$SKIP_CONNECT" != "1" ]; then
        connect_emulator
    else
        ensure_single_device
    fi
    ensure_adb_shell_service || exit 1
    wait_boot_completed

    if [ -n "$APK_PATH" ]; then
        [ -f "$APK_PATH" ] || die "APK not found: $APK_PATH"
        note "installing APK: $APK_PATH"
        adb_base install -r "$APK_PATH"
    fi

    if [ -n "$MODULE_ZIP" ]; then
        [ -f "$MODULE_ZIP" ] || die "module ZIP not found: $MODULE_ZIP"
        [ "$ALLOW_MODULE_INSTALL" = "1" ] || die "module install is mutating; pass --allow-module-install"
        ensure_root_shell
        adb_su "command -v magisk >/dev/null 2>&1" || die "magisk command not found on emulator"
        remote_zip="/data/local/tmp/privstack-module.zip"
        note "pushing module ZIP to $remote_zip"
        adb_base push "$MODULE_ZIP" "$remote_zip"
        note "installing Magisk module"
        adb_su "magisk --install-module '$remote_zip'"
    fi

    if [ "$REBOOT_AFTER_INSTALL" = "1" ]; then
        note "rebooting emulator after install"
        adb_base reboot
        sleep 2
        connect_emulator
        wait_boot_completed
    fi
}

case "$command_name" in
    start)
        start_vm
        ;;
    prepare-insecure-adb)
        prepare_insecure_adb
        ;;
    connect)
        connect_emulator
        ensure_adb_shell_service || exit 1
        wait_boot_completed
        ;;
    doctor)
        connect_emulator
        ensure_adb_shell_service || exit 1
        wait_boot_completed
        note "adb shell service is available"
        ;;
    collect)
        run_collect
        ;;
    smoke)
        run_smoke
        ;;
    install)
        install_artifacts
        ;;
    ""|-h|--help)
        usage
        ;;
    *)
        die "unknown command: $command_name"
        ;;
esac
