#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
manifest="${repo_root}/app/app/src/main/AndroidManifest.xml"
kotlin_src="${repo_root}/app/app/src/main/kotlin"
profile_config="${repo_root}/app/app/src/main/kotlin/com/rknnovpn/panel/model/ProfileConfig.kt"
daemon_config="${repo_root}/daemon/internal/config/config.go"
module_default_config="${repo_root}/module/defaults/config.json"

fail=0

check_absent_file() {
  local label="$1"
  local pattern="$2"
  local file="$3"

  if grep -En -- "${pattern}" "${file}"; then
    echo "::error file=${file},title=${label}::Forbidden APK privacy surface detected"
    fail=1
  fi
}

check_absent_tree() {
  local label="$1"
  local pattern="$2"
  local dir="$3"

  if grep -REn --include='*.kt' -- "${pattern}" "${dir}"; then
    echo "::error title=${label}::Forbidden direct APK networking API detected"
    fail=1
  fi
}

check_present_file() {
  local label="$1"
  local pattern="$2"
  local file="$3"

  if ! grep -Eq -- "${pattern}" "${file}"; then
    echo "::error file=${file},title=${label}::Expected privacy-preserving default is missing"
    fail=1
  fi
}

check_absent_file "No INTERNET permission" 'android[.]permission[.]INTERNET' "${manifest}"
check_absent_file "No ACCESS_NETWORK_STATE permission" 'android[.]permission[.]ACCESS_NETWORK_STATE' "${manifest}"
check_absent_file "No VPN service permission" 'android[.]permission[.]BIND_VPN_SERVICE' "${manifest}"
check_absent_file "No VpnService declaration" 'android[.]net[.]VpnService|foregroundServiceType="[^"]*(vpn|dataSync)' "${manifest}"

check_absent_tree "No direct APK HTTP client" 'HttpURLConnection|OkHttpClient|Retrofit|import java[.]net[.]URL$|java[.]net[.]URL[ (]' "${kotlin_src}"

check_present_file "SOCKS helper disabled by default" 'val socksPort: Int = 0' "${profile_config}"
check_present_file "HTTP helper disabled by default" 'val httpPort: Int = 0' "${profile_config}"
check_present_file "TUN disabled by default" 'val enabled: Boolean = false' "${profile_config}"
check_present_file "Clash API disabled by default" 'APIPort:[[:space:]]+0' "${daemon_config}"
check_absent_file "No default local helper ports in module config" '10808|10809|9090|"api_port"[[:space:]]*:[[:space:]]*[1-9]' "${module_default_config}"
if [ -e "${repo_root}/module/defaults/panel.json" ]; then
  echo "::error file=${repo_root}/module/defaults/panel.json,title=No panel defaults::panel.json is not a supported v2 storage artifact"
  fail=1
fi

exit "${fail}"
