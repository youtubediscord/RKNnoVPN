#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() {
  echo "release manifest: $*" >&2
  exit 1
}

version_code() {
  local raw="${1#v}"
  local major minor patch extra
  IFS=. read -r major minor patch extra <<<"$raw"
  [[ -z "${extra:-}" ]] || fail "unsupported version with more than three numeric components: $1"
  [[ "${major:-}" =~ ^[0-9]+$ ]] || fail "invalid major version in $1"
  [[ "${minor:-}" =~ ^[0-9]+$ ]] || fail "invalid minor version in $1"
  [[ "${patch:-}" =~ ^[0-9]+$ ]] || fail "invalid patch version in $1"
  echo $((major * 1000 + minor * 100 + patch))
}

extract_go_version() {
  local file="$1"
  sed -n 's/^[[:space:]]*\(var[[:space:]]\+\)\{0,1\}Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\2/p' "$file" | head -n 1
}

daemon_version="$(extract_go_version daemon/cmd/daemon/main.go)"
ctl_version="$(extract_go_version daemon/cmd/daemonctl/main.go)"
apk_version="$(sed -n 's/^[[:space:]]*versionName[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' app/app/build.gradle.kts | head -n 1)"
apk_code="$(sed -n 's/^[[:space:]]*versionCode[[:space:]]*=[[:space:]]*\([0-9][0-9]*\).*/\1/p' app/app/build.gradle.kts | head -n 1)"
module_version="$(sed -n 's/^version=//p' module/module.prop | head -n 1)"
module_code="$(sed -n 's/^versionCode=//p' module/module.prop | head -n 1)"
update_version="$(sed -n 's/[[:space:]]*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' update.json | head -n 1)"
update_code="$(sed -n 's/[[:space:]]*"versionCode"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' update.json | head -n 1)"

[ -n "$daemon_version" ] || fail "daemon Version not found"
[ "$ctl_version" = "$daemon_version" ] || fail "daemonctl version $ctl_version does not match daemon $daemon_version"
[ "$apk_version" = "$daemon_version" ] || fail "APK version $apk_version does not match $daemon_version"
[ "$module_version" = "$daemon_version" ] || fail "module version $module_version does not match $daemon_version"
[ "$update_version" = "$daemon_version" ] || fail "update.json version $update_version does not match $daemon_version"

expected_code="$(version_code "$daemon_version")"
[ "$apk_code" = "$expected_code" ] || fail "APK versionCode $apk_code does not match expected $expected_code"
[ "$module_code" = "$expected_code" ] || fail "module versionCode $module_code does not match expected $expected_code"
[ "$update_code" = "$expected_code" ] || fail "update.json versionCode $update_code does not match expected $expected_code"

for required in \
  module/scripts/lib/rknnovpn_env.sh \
  module/scripts/lib/rknnovpn_install.sh \
  module/scripts/lib/rknnovpn_installer_flow.sh \
  module/scripts/lib/rknnovpn_netstack.sh \
  module/scripts/lib/rknnovpn_iptables_rules.sh \
  module/scripts/rescue_reset.sh \
  module/scripts/routing.sh \
  module/scripts/iptables.sh \
  module/scripts/dns.sh \
  module/OWNERSHIP.md \
  module/customize.sh \
  module/service.sh \
  module/post-fs-data.sh \
  module/uninstall.sh; do
  [ -f "$required" ] || fail "required module file missing: $required"
done

expected_zip="https://github.com/youtubediscord/RKNnoVPN/releases/download/${daemon_version}/rknnovpn-${daemon_version}-module.zip"
expected_changelog="https://github.com/youtubediscord/RKNnoVPN/releases/tag/${daemon_version}"
rg -q "\"zipUrl\"[[:space:]]*:[[:space:]]*\"${expected_zip}\"" update.json || fail "update.json zipUrl is not $expected_zip"
rg -q "\"changelog\"[[:space:]]*:[[:space:]]*\"${expected_changelog}\"" update.json || fail "update.json changelog is not $expected_changelog"

if rg -n "s/v//;s/\\\\?\\.//g|s/v//;s/\\.//g" .github/workflows Makefile >/tmp/release-manifest-old-code.$$ 2>/dev/null; then
  cat /tmp/release-manifest-old-code.$$ >&2
  rm -f /tmp/release-manifest-old-code.$$
  fail "old dot-stripping versionCode formula is forbidden"
fi
rm -f /tmp/release-manifest-old-code.$$

if [ "$(version_code v1.8.0)" != "1800" ] || [ "$(version_code v1.7.13)" != "1713" ]; then
  fail "canonical version_code formula is not monotonic for v1.8.0 over v1.7.13"
fi

echo "release manifest: ${daemon_version} (${expected_code}) ok"
