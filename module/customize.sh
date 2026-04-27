#!/system/bin/sh
# RKNnoVPN — Magisk/KSU/APatch installation entrypoint
# POSIX sh compatible (busybox ash)

RKNNOVPN_DIR="/data/adb/rknnovpn"
RKNNOVPN_GID=23333
MODULE_ID="rknnovpn"
SUBDIRS="bin config config/rendered scripts run logs backup profiles releases"

if [ -n "${MODPATH:-}" ] && [ -f "${MODPATH}/scripts/lib/rknnovpn_env.sh" ]; then
    . "${MODPATH}/scripts/lib/rknnovpn_env.sh"
fi
if [ -n "${MODPATH:-}" ] && [ -f "${MODPATH}/scripts/lib/rknnovpn_install.sh" ]; then
    . "${MODPATH}/scripts/lib/rknnovpn_install.sh"
fi
if [ -n "${MODPATH:-}" ] && [ -f "${MODPATH}/scripts/lib/rknnovpn_installer_flow.sh" ]; then
    . "${MODPATH}/scripts/lib/rknnovpn_installer_flow.sh"
fi

if ! command -v rknnovpn_installer_run >/dev/null 2>&1; then
    ui_print "  [!] FATAL: installer flow library missing"
    abort "scripts/lib/rknnovpn_installer_flow.sh missing from module archive"
fi

rknnovpn_installer_run
