#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

fail() {
  echo "reset.lock contract: $*" >&2
  exit 1
}

if rg -n 'rm -f .*reset\.lock|reset\.lock.*rm -f|RESET_LOCK.*rm -f' module/post-fs-data.sh module/service.sh >/tmp/reset-lock-forbidden.$$ 2>/dev/null; then
  cat /tmp/reset-lock-forbidden.$$ >&2
  rm -f /tmp/reset-lock-forbidden.$$
  fail "post-fs-data.sh/service.sh must not remove reset.lock directly; use rescue_reset.sh boot-clean/hard-reset"
fi
rm -f /tmp/reset-lock-forbidden.$$

if rg -n 'rm -f .*run/(active|daemon\.pid|singbox\.pid|daemon\.sock)|run/(active|daemon\.pid|singbox\.pid|daemon\.sock).*rm -f' module/post-fs-data.sh >/tmp/runtime-marker-forbidden.$$ 2>/dev/null; then
  cat /tmp/runtime-marker-forbidden.$$ >&2
  rm -f /tmp/runtime-marker-forbidden.$$
  fail "post-fs-data.sh must leave runtime markers for service.sh boot cleanup"
fi
rm -f /tmp/runtime-marker-forbidden.$$

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fakebin="$tmp/bin"
mkdir -p "$fakebin"
for cmd in ip iptables ip6tables iptables-legacy ip6tables-legacy iptables-nft ip6tables-nft sleep; do
  cat >"$fakebin/$cmd" <<'EOF'
#!/usr/bin/env sh
case "${0##*/}" in
  sleep)
    exit 0
    ;;
  ip)
    case "$*" in
      *" show "*|*" rule show "*|*" route show "*)
        exit 0
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  *)
    if [ "$1" = "-w" ]; then
      shift 2
    fi
    case "$*" in
      *" -S"*|"-S"*|*" -t "*"-S"*)
        exit 0
        ;;
      *)
        exit 0
        ;;
    esac
    ;;
esac
EOF
  chmod 755 "$fakebin/$cmd"
done

run_rescue() {
  local mode="$1"
  local data="$tmp/data-$mode"
  mkdir -p "$data/bin" "$data/run" "$data/config" "$data/logs"
  PATH="$fakebin:$PATH" RKNNOVPN_DIR="$data" sh module/scripts/rescue_reset.sh "$mode" >/dev/null
  printf '%s\n' "$data"
}

data="$(run_rescue daemon-reset)"
[ -f "$data/run/reset.lock" ] || fail "daemon-reset must leave reset.lock for the daemon-owned reset window"
[ -f "$data/config/manual" ] || fail "daemon-reset must leave manual flag"

data="$(run_rescue hard-reset)"
[ ! -e "$data/run/reset.lock" ] || fail "hard-reset must remove reset.lock when cleanup finishes"
[ -f "$data/config/manual" ] || fail "hard-reset must leave manual flag"

data="$(run_rescue boot-clean)"
[ ! -e "$data/run/reset.lock" ] || fail "boot-clean must remove reset.lock when cleanup finishes"
[ ! -e "$data/config/manual" ] || fail "boot-clean must not create manual flag"

data="$(run_rescue uninstall-clean)"
[ ! -e "$data/run/reset.lock" ] || fail "uninstall-clean must remove reset.lock when cleanup finishes"
[ ! -e "$data/config/manual" ] || fail "uninstall-clean must not create manual flag"

echo "reset.lock contract: ok"
