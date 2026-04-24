#!/system/bin/sh
# PrivStack — Magisk/KSU/APatch installation script
# POSIX sh compatible (busybox ash)

# ============================================================================
# Constants
# ============================================================================

PRIVSTACK_DIR="/data/adb/privstack"
PRIVSTACK_GID=23333
MODULE_ID="privstack"

# Subdirectories under /data/adb/privstack/
SUBDIRS="bin config config/rendered scripts run logs backup profiles releases"

# ============================================================================
# Helpers
# ============================================================================

abort_install() {
    ui_print "  [!] FATAL: $1"
    abort "$1"
}

ui_print_header() {
    ui_print ""
    ui_print "  ==========================================="
    ui_print "   PrivStack v1.0.0 — Transparent Proxy"
    ui_print "   tproxy + iptables + sing-box"
    ui_print "  ==========================================="
    ui_print ""
}

# Detect which root manager is running the installer
detect_root_manager() {
    # KernelSU sets KSU=true in environment
    if [ -n "$KSU" ] && [ "$KSU" = "true" ]; then
        ROOT_MGR="kernelsu"
        BUSYBOX="/data/adb/ksu/bin/busybox"
        ui_print "  [*] Root manager: KernelSU"
        return
    fi

    # APatch sets APATCH=true in environment
    if [ -n "$APATCH" ] && [ "$APATCH" = "true" ]; then
        ROOT_MGR="apatch"
        BUSYBOX="/data/adb/ap/bin/busybox"
        ui_print "  [*] Root manager: APatch"
        return
    fi

    # Magisk sets MAGISK_VER in environment; also check for MAGISKTMP
    if [ -n "$MAGISK_VER" ] || [ -d "/data/adb/magisk" ]; then
        ROOT_MGR="magisk"
        BUSYBOX="/data/adb/magisk/busybox"
        ui_print "  [*] Root manager: Magisk $MAGISK_VER"
        return
    fi

    # Fallback — assume Magisk-compatible
    ROOT_MGR="unknown"
    BUSYBOX="busybox"
    ui_print "  [!] Root manager: unknown (assuming Magisk-compatible)"
}

# ============================================================================
# Pre-flight checks
# ============================================================================

check_architecture() {
    ARCH="$(getprop ro.product.cpu.abi)"
    case "$ARCH" in
        arm64-v8a|arm64*)
            ARCH_DIR="arm64"
            ui_print "  [*] Architecture: $ARCH -> ${ARCH_DIR} (OK)"
            ;;
        armeabi-v7a|armeabi|armv7*|arm*)
            ARCH_DIR="armv7"
            ui_print "  [*] Architecture: $ARCH -> ${ARCH_DIR} (OK)"
            ;;
        *)
            abort_install "Unsupported architecture: $ARCH. Supported: arm64-v8a and armeabi-v7a."
            ;;
    esac
}

check_api_level() {
    API="$(getprop ro.build.version.sdk)"
    if [ -z "$API" ]; then
        abort_install "Cannot determine Android API level."
    fi
    if [ "$API" -lt 28 ]; then
        abort_install "Android API $API < 28. Minimum Android 9 (Pie) required."
    fi
    ui_print "  [*] API level: $API (>= 28, OK)"
}

check_tproxy_support() {
    ui_print "  [*] Checking TPROXY kernel support..."

    TPROXY_OK=0

    # Method 1: check /proc/config.gz if available
    if [ -f "/proc/config.gz" ]; then
        if command -v zcat >/dev/null 2>&1; then
            if zcat /proc/config.gz 2>/dev/null | grep -q "CONFIG_NETFILTER_XT_TARGET_TPROXY="; then
                TPROXY_RESULT="$(zcat /proc/config.gz 2>/dev/null | grep 'CONFIG_NETFILTER_XT_TARGET_TPROXY=')"
                case "$TPROXY_RESULT" in
                    *=y|*=m)
                        TPROXY_OK=1
                        ui_print "  [*] TPROXY: $TPROXY_RESULT (OK)"
                        ;;
                    *)
                        ui_print "  [!] TPROXY: $TPROXY_RESULT (disabled)"
                        ;;
                esac
            else
                ui_print "  [!] TPROXY config not found in /proc/config.gz"
            fi
        elif command -v gzip >/dev/null 2>&1; then
            if gzip -dc /proc/config.gz 2>/dev/null | grep -q "CONFIG_NETFILTER_XT_TARGET_TPROXY="; then
                TPROXY_RESULT="$(gzip -dc /proc/config.gz 2>/dev/null | grep 'CONFIG_NETFILTER_XT_TARGET_TPROXY=')"
                case "$TPROXY_RESULT" in
                    *=y|*=m)
                        TPROXY_OK=1
                        ui_print "  [*] TPROXY: $TPROXY_RESULT (OK)"
                        ;;
                    *)
                        ui_print "  [!] TPROXY: $TPROXY_RESULT (disabled)"
                        ;;
                esac
            fi
        fi
    fi

    # Method 2: try loading the xt_TPROXY module
    if [ "$TPROXY_OK" -eq 0 ]; then
        if [ -f "/proc/net/ip_tables_targets" ]; then
            if grep -q "TPROXY" /proc/net/ip_tables_targets 2>/dev/null; then
                TPROXY_OK=1
                ui_print "  [*] TPROXY: found in ip_tables_targets (OK)"
            fi
        fi
    fi

    # Method 3: attempt modprobe
    if [ "$TPROXY_OK" -eq 0 ]; then
        modprobe xt_TPROXY 2>/dev/null
        if [ -f "/proc/net/ip_tables_targets" ] && grep -q "TPROXY" /proc/net/ip_tables_targets 2>/dev/null; then
            TPROXY_OK=1
            ui_print "  [*] TPROXY: loaded via modprobe (OK)"
        fi
    fi

    if [ "$TPROXY_OK" -eq 0 ]; then
        ui_print ""
        ui_print "  [!] WARNING: TPROXY support not confirmed."
        ui_print "  [!] The module will be installed, but may not"
        ui_print "  [!] work if your kernel lacks TPROXY."
        ui_print ""
    fi
}

# ============================================================================
# Directory and file installation
# ============================================================================

create_directory_structure() {
    ui_print "  [*] Creating directory structure..."

    for subdir in $SUBDIRS; do
        mkdir -p "${PRIVSTACK_DIR}/${subdir}" 2>/dev/null
        if [ ! -d "${PRIVSTACK_DIR}/${subdir}" ]; then
            abort_install "Failed to create ${PRIVSTACK_DIR}/${subdir}"
        fi
    done

    ui_print "  [*] Directories created under ${PRIVSTACK_DIR}/"
}

preserve_existing_config() {
    CONFIG_FILE="${PRIVSTACK_DIR}/config/config.json"
    PANEL_FILE="${PRIVSTACK_DIR}/config/panel.json"

    if [ -f "$CONFIG_FILE" ]; then
        ui_print "  [*] Existing config.json found — preserving"
        cp -f "$CONFIG_FILE" "${PRIVSTACK_DIR}/backup/config.json.pre-upgrade" 2>/dev/null
        PRESERVE_CONFIG=1
    else
        PRESERVE_CONFIG=0
        ui_print "  [*] No existing config — will install defaults"
    fi

    if [ -f "$PANEL_FILE" ]; then
        ui_print "  [*] Existing panel.json found — preserving"
        cp -f "$PANEL_FILE" "${PRIVSTACK_DIR}/backup/panel.json.pre-upgrade" 2>/dev/null
        PRESERVE_PANEL=1
    else
        PRESERVE_PANEL=0
        ui_print "  [*] No existing panel.json — will install defaults when available"
    fi
}

install_default_config() {
    CONFIG_FILE="${PRIVSTACK_DIR}/config/config.json"
    PANEL_FILE="${PRIVSTACK_DIR}/config/panel.json"

    if [ "$PRESERVE_CONFIG" -eq 0 ]; then
        if [ -f "${MODPATH}/defaults/config.json" ]; then
            cp -f "${MODPATH}/defaults/config.json" "$CONFIG_FILE"
            ui_print "  [*] Default config.json installed"
        else
            abort_install "defaults/config.json missing from module archive"
        fi
    fi

    # Always update the defaults reference copy
    if [ -f "${MODPATH}/defaults/config.json" ]; then
        cp -f "${MODPATH}/defaults/config.json" "${PRIVSTACK_DIR}/config/config.defaults.json"
    fi

    if [ "$PRESERVE_PANEL" -eq 0 ] && [ -f "${MODPATH}/defaults/panel.json" ]; then
        cp -f "${MODPATH}/defaults/panel.json" "$PANEL_FILE"
        ui_print "  [*] Default panel.json installed"
    fi

    if [ -f "${MODPATH}/defaults/panel.json" ]; then
        cp -f "${MODPATH}/defaults/panel.json" "${PRIVSTACK_DIR}/config/panel.defaults.json"
    fi
}

install_binaries() {
    SRC_BIN="${MODPATH}/binaries/${ARCH_DIR:-arm64}"

    if [ ! -d "$SRC_BIN" ]; then
        abort_install "No binaries directory for ${ARCH:-unknown} at ${SRC_BIN}"
    fi

    ui_print "  [*] Installing ${ARCH_DIR:-arm64} binaries..."

    for bin_file in "$SRC_BIN"/*; do
        [ ! -f "$bin_file" ] && continue
        bin_name="$(basename "$bin_file")"
        cp -f "$bin_file" "${PRIVSTACK_DIR}/bin/${bin_name}"
        ui_print "  [*]   -> ${bin_name}"
    done
}

install_scripts() {
    SRC_SCRIPTS="${MODPATH}/scripts"

    if [ ! -d "$SRC_SCRIPTS" ]; then
        ui_print "  [!] No scripts directory at ${SRC_SCRIPTS}"
        ui_print "  [!] Skipping scripts installation"
        return
    fi

    ui_print "  [*] Installing scripts..."

    for script_file in "$SRC_SCRIPTS"/*; do
        [ ! -f "$script_file" ] && continue
        script_name="$(basename "$script_file")"
        cp -f "$script_file" "${PRIVSTACK_DIR}/scripts/${script_name}"
        ui_print "  [*]   -> ${script_name}"
    done
}

file_sha256() {
    file="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$file" 2>/dev/null | awk '{print $1}'
        return $?
    fi
    if [ -n "$BUSYBOX" ] && "$BUSYBOX" sha256sum "$file" >/dev/null 2>&1; then
        "$BUSYBOX" sha256sum "$file" 2>/dev/null | awk '{print $1}'
        return $?
    fi
    if command -v toybox >/dev/null 2>&1; then
        toybox sha256sum "$file" 2>/dev/null | awk '{print $1}'
        return $?
    fi
    return 1
}

module_version() {
    if [ -f "${MODPATH}/module.prop" ]; then
        awk -F= '$1=="version"{print $2; exit}' "${MODPATH}/module.prop" 2>/dev/null
    fi
}

safe_release_name() {
    raw="$1"
    safe="$(echo "$raw" | sed 's/[^A-Za-z0-9._-]/_/g; s/^[._-]*//; s/[._-]*$//')"
    [ -n "$safe" ] || safe="unknown"
    echo "$safe"
}

copy_if_present() {
    src="$1"
    dst="$2"
    if [ -f "$src" ]; then
        mkdir -p "$(dirname "$dst")" 2>/dev/null
        cp -f "$src" "$dst"
    fi
}

append_manifest_hash() {
    rel="$1"
    file="$2"
    manifest="$3"
    [ -f "$file" ] || return 0
    hash="$(file_sha256 "$file")" || return 1
    if [ "$MANIFEST_FIRST" -eq 0 ]; then
        printf ',\n' >> "$manifest"
    fi
    MANIFEST_FIRST=0
    printf '    "%s": "%s"' "$rel" "$hash" >> "$manifest"
}

write_install_manifest() {
    release_dir="$1"
    version="$2"
    manifest="${release_dir}/install-manifest.json"
    tmp_manifest="${manifest}.tmp"
    MANIFEST_FIRST=1

    {
        printf '{\n'
        printf '  "version": "%s",\n' "$version"
        printf '  "installed_at": "%s",\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || date '+%Y-%m-%dT%H:%M:%SZ')"
        printf '  "files_sha256": {\n'
    } > "$tmp_manifest" || return 1

    for rel in \
        bin/privd \
        bin/privctl \
        bin/sing-box \
        module/module.prop \
        module/service.sh \
        module/post-fs-data.sh \
        module/uninstall.sh \
        module/customize.sh \
        module/scripts/dns.sh \
        module/scripts/iptables.sh \
        module/scripts/net_handler.sh \
        module/scripts/rescue_reset.sh \
        module/scripts/routing.sh \
        module/defaults/config.json \
        module/defaults/panel.json; do
        append_manifest_hash "$rel" "${release_dir}/${rel}" "$tmp_manifest" || return 1
    done

    {
        printf '\n'
        printf '  }\n'
        printf '}\n'
    } >> "$tmp_manifest" || return 1
    mv -f "$tmp_manifest" "$manifest"
}

install_release_catalog() {
    version="$(module_version)"
    [ -n "$version" ] || version="unknown"
    safe_version="$(safe_release_name "$version")"
    release_dir="${PRIVSTACK_DIR}/releases/${safe_version}"
    if [ -e "$release_dir" ]; then
        suffix="$(date +%s 2>/dev/null || echo $$)"
        release_dir="${release_dir}-${suffix}"
    fi

    ui_print "  [*] Recording versioned release: ${version}"
    mkdir -p "${release_dir}/bin" "${release_dir}/module/scripts" "${release_dir}/module/defaults" || {
        ui_print "  [!] Failed to create release catalog"
        return
    }

    for bin_name in sing-box privd privctl; do
        copy_if_present "${PRIVSTACK_DIR}/bin/${bin_name}" "${release_dir}/bin/${bin_name}"
    done
    for name in module.prop service.sh post-fs-data.sh uninstall.sh customize.sh sepolicy.rule; do
        copy_if_present "${MODPATH}/${name}" "${release_dir}/module/${name}"
    done
    if [ -d "${MODPATH}/scripts" ]; then
        for script_file in "${MODPATH}/scripts"/*; do
            [ -f "$script_file" ] && copy_if_present "$script_file" "${release_dir}/module/scripts/$(basename "$script_file")"
        done
    fi
    if [ -d "${MODPATH}/defaults" ]; then
        for default_file in "${MODPATH}/defaults"/*; do
            [ -f "$default_file" ] && copy_if_present "$default_file" "${release_dir}/module/defaults/$(basename "$default_file")"
        done
    fi

    if write_install_manifest "$release_dir" "$version"; then
        ln -sfn "$release_dir" "${PRIVSTACK_DIR}/current" 2>/dev/null || ui_print "  [!] Failed to update current release link"
    else
        ui_print "  [!] Failed to write release manifest"
    fi
}

# ============================================================================
# Permissions
# ============================================================================

set_permissions_and_caps() {
    ui_print "  [*] Setting permissions..."

    # Ensure the proxy GID group exists conceptually (Android doesn't use
    # /etc/group the same way — the GID is used numerically in iptables rules).

    # Binaries: 0750 root:PRIVSTACK_GID — executable by root and proxy group
    if [ -d "${PRIVSTACK_DIR}/bin" ]; then
        chown -R 0:${PRIVSTACK_GID} "${PRIVSTACK_DIR}/bin" 2>/dev/null
        chmod 0750 "${PRIVSTACK_DIR}/bin" 2>/dev/null
        for f in "${PRIVSTACK_DIR}/bin"/*; do
            [ -f "$f" ] && chmod 0750 "$f" 2>/dev/null
        done
    fi

    # Scripts: 0755 root:root — world-readable, owner-executable
    if [ -d "${PRIVSTACK_DIR}/scripts" ]; then
        chown -R 0:0 "${PRIVSTACK_DIR}/scripts" 2>/dev/null
        chmod 0755 "${PRIVSTACK_DIR}/scripts" 2>/dev/null
        for f in "${PRIVSTACK_DIR}/scripts"/*; do
            [ -f "$f" ] && chmod 0755 "$f" 2>/dev/null
        done
    fi

    # Config: 0600 root:root — sensitive (may contain credentials)
    if [ -d "${PRIVSTACK_DIR}/config" ]; then
        chown -R 0:0 "${PRIVSTACK_DIR}/config" 2>/dev/null
        chmod 0700 "${PRIVSTACK_DIR}/config" 2>/dev/null
        for f in "${PRIVSTACK_DIR}/config"/*; do
            [ -f "$f" ] && chmod 0600 "$f" 2>/dev/null
        done
        # Rendered subdir
        chmod 0700 "${PRIVSTACK_DIR}/config/rendered" 2>/dev/null
    fi

    # Logs: 0700 root:root — may contain proxy endpoints and diagnostics.
    chown -R 0:0 "${PRIVSTACK_DIR}/logs" 2>/dev/null
    chmod 0700 "${PRIVSTACK_DIR}/logs" 2>/dev/null
    for f in "${PRIVSTACK_DIR}/logs"/*; do
        [ -f "$f" ] && chmod 0600 "$f" 2>/dev/null
    done

    # Run: 0750 root:PRIVSTACK_GID (PID files, sockets)
    chown -R 0:${PRIVSTACK_GID} "${PRIVSTACK_DIR}/run" 2>/dev/null
    chmod 0750 "${PRIVSTACK_DIR}/run" 2>/dev/null

    # Backup and profiles: 0700 root:root
    chown -R 0:0 "${PRIVSTACK_DIR}/backup" 2>/dev/null
    chmod 0700 "${PRIVSTACK_DIR}/backup" 2>/dev/null
    chown -R 0:0 "${PRIVSTACK_DIR}/profiles" 2>/dev/null
    chmod 0700 "${PRIVSTACK_DIR}/profiles" 2>/dev/null

    # Top-level data dir
    chown 0:0 "${PRIVSTACK_DIR}" 2>/dev/null
    chmod 0755 "${PRIVSTACK_DIR}" 2>/dev/null

    ui_print "  [*] Permissions set"

    # Set Linux capabilities on binaries if setcap is available
    set_capabilities
}

set_capabilities() {
    SETCAP=""

    # Find setcap binary
    if command -v setcap >/dev/null 2>&1; then
        SETCAP="setcap"
    elif [ -x "/system/bin/setcap" ]; then
        SETCAP="/system/bin/setcap"
    elif [ -x "${PRIVSTACK_DIR}/bin/setcap" ]; then
        SETCAP="${PRIVSTACK_DIR}/bin/setcap"
    fi

    if [ -z "$SETCAP" ]; then
        ui_print "  [!] setcap not found — skipping capability assignment"
        ui_print "  [!] Daemon will rely on running as root"
        return
    fi

    ui_print "  [*] Setting capabilities with setcap..."

    # sing-box needs: net_admin (tproxy/iptables), net_raw (raw sockets),
    # net_bind_service (bind < 1024)
    CAPS="cap_net_admin,cap_net_raw,cap_net_bind_service+ep"

    for bin_name in sing-box privd; do
        bin_path="${PRIVSTACK_DIR}/bin/${bin_name}"
        if [ -f "$bin_path" ]; then
            $SETCAP "$CAPS" "$bin_path" 2>/dev/null
            if [ $? -eq 0 ]; then
                ui_print "  [*]   ${bin_name}: capabilities set"
            else
                ui_print "  [!]   ${bin_name}: setcap failed (will run as root)"
            fi
        fi
    done
}

# ============================================================================
# Module path permissions (for Magisk overlay)
# ============================================================================

set_module_permissions() {
    # Standard Magisk module permissions
    set_perm_recursive "$MODPATH" 0 0 0755 0644
    # Make shell scripts executable inside the module overlay
    for f in "$MODPATH"/*.sh; do
        [ -f "$f" ] && set_perm "$f" 0 0 0755
    done
}

# ============================================================================
# Main installation flow
# ============================================================================

ui_print_header

# Step 1: Detect root manager
detect_root_manager

# Step 2: Validate device
ui_print "  --- Pre-flight checks ---"
check_architecture
check_api_level
check_tproxy_support

# Step 3: Create data directory structure
ui_print ""
ui_print "  --- Installation ---"
create_directory_structure

# Step 4: Preserve existing config on upgrade
preserve_existing_config

# Step 5: Install default config (or skip if preserved)
install_default_config

# Step 6: Install binaries
install_binaries

# Step 7: Install scripts
install_scripts

# Step 8: Set permissions and capabilities
set_permissions_and_caps

# Step 9: Record versioned release catalog
install_release_catalog

# Step 10: Module overlay permissions
set_module_permissions

# Done
ui_print ""
ui_print "  --- Installation complete ---"
ui_print ""
ui_print "  Data directory: ${PRIVSTACK_DIR}/"
ui_print "  Config file:    ${PRIVSTACK_DIR}/config/config.json"
ui_print "  Daemon binary:  ${PRIVSTACK_DIR}/bin/privd"
ui_print "  Core binary:    ${PRIVSTACK_DIR}/bin/sing-box"
ui_print ""
if [ "$PRESERVE_CONFIG" -eq 1 ]; then
    ui_print "  [*] Existing config was preserved."
    ui_print "  [*] Backup at: ${PRIVSTACK_DIR}/backup/config.json.pre-upgrade"
fi
ui_print ""
ui_print "  Reboot to activate PrivStack."
ui_print ""
