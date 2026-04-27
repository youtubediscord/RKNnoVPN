#!/system/bin/sh
# Shared installer/release-catalog helpers for module and hot-update layouts.

privstack_install_copy_tree() {
    _src="$1"
    _dst="$2"
    [ -d "$_src" ] || return 0
    find "$_src" -type f 2>/dev/null | while IFS= read -r _file; do
        [ -f "$_file" ] || continue
        _rel="${_file#$_src/}"
        _target="${_dst}/${_rel}"
        mkdir -p "$(dirname "$_target")" 2>/dev/null || return 1
        cp -f "$_file" "$_target" || return 1
    done
}

privstack_install_file_sha256() {
    _file="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$_file" 2>/dev/null | awk '{print $1}'
        return $?
    fi
    if [ -n "${BUSYBOX:-}" ] && "$BUSYBOX" sha256sum "$_file" >/dev/null 2>&1; then
        "$BUSYBOX" sha256sum "$_file" 2>/dev/null | awk '{print $1}'
        return $?
    fi
    if command -v toybox >/dev/null 2>&1; then
        toybox sha256sum "$_file" 2>/dev/null | awk '{print $1}'
        return $?
    fi
    return 1
}

privstack_module_version() {
    _module_path="${1:-${MODPATH:-}}"
    if [ -n "$_module_path" ] && [ -f "${_module_path}/module.prop" ]; then
        awk -F= '$1=="version"{print $2; exit}' "${_module_path}/module.prop" 2>/dev/null
    fi
}

privstack_safe_release_name() {
    _raw="$1"
    _safe="$(echo "$_raw" | sed 's/[^A-Za-z0-9._-]/_/g; s/^[._-]*//; s/[._-]*$//')"
    [ -n "$_safe" ] || _safe="unknown"
    echo "$_safe"
}

privstack_copy_if_present() {
    _src="$1"
    _dst="$2"
    if [ -f "$_src" ]; then
        mkdir -p "$(dirname "$_dst")" 2>/dev/null || return 1
        cp -f "$_src" "$_dst"
    fi
}

privstack_manifest_hash_entry() {
    _rel="$1"
    _file="$2"
    _manifest="$3"
    [ -f "$_file" ] || return 0
    _hash="$(privstack_install_file_sha256 "$_file")" || return 1
    if [ "$MANIFEST_FIRST" -eq 0 ]; then
        printf ',\n' >> "$_manifest"
    fi
    MANIFEST_FIRST=0
    printf '    "%s": "%s"' "$_rel" "$_hash" >> "$_manifest"
}
