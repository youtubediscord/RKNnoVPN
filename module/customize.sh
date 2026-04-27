#!/system/bin/sh
# PrivStack — Magisk/KSU/APatch installation entrypoint
# POSIX sh compatible (busybox ash)

PRIVSTACK_DIR="/data/adb/privstack"
PRIVSTACK_GID=23333
MODULE_ID="privstack"
SUBDIRS="bin config config/rendered scripts run logs backup profiles releases"

if [ -n "${MODPATH:-}" ] && [ -f "${MODPATH}/scripts/lib/privstack_env.sh" ]; then
    . "${MODPATH}/scripts/lib/privstack_env.sh"
fi
if [ -n "${MODPATH:-}" ] && [ -f "${MODPATH}/scripts/lib/privstack_install.sh" ]; then
    . "${MODPATH}/scripts/lib/privstack_install.sh"
fi
if [ -n "${MODPATH:-}" ] && [ -f "${MODPATH}/scripts/lib/privstack_installer_flow.sh" ]; then
    . "${MODPATH}/scripts/lib/privstack_installer_flow.sh"
fi

if ! command -v privstack_installer_run >/dev/null 2>&1; then
    ui_print "  [!] FATAL: installer flow library missing"
    abort "scripts/lib/privstack_installer_flow.sh missing from module archive"
fi

privstack_installer_run
