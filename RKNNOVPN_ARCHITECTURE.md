# Архитектура RKNnoVPN

## Назначение

RKNnoVPN - прозрачный прокси-стек для rooted Android. Цель проекта: маршрутизировать трафик выбранных приложений через прокси без Android `VpnService`, TUN-интерфейса и системной VPN-индикации.

Система состоит из двух частей:

1. Magisk/KernelSU/APatch-модуль с root-демоном `privd`, CLI `privctl`, скриптами iptables и бинарником `sing-box`.
2. Android APK, который управляет демоном через `su -c privctl`; HTTP/control-plane операции выполняет root-демон, чтобы APK не зависел от собственной сети и Android VPN stack.

## Основной поток трафика

```text
Android app
  -> iptables mangle OUTPUT / PREROUTING
  -> fwmark
  -> policy route table
  -> local TPROXY socket
  -> sing-box tproxy inbound
  -> urltest/selector/outbound
  -> upstream proxy
```

В текущей реализации маршрут по умолчанию внутри `sing-box` указывает на outbound `proxy`. Если сохранён один node, `proxy` является прямым outbound этого node. Если nodes несколько, `proxy` становится `urltest` outbound, который выбирает один из node-outbounds.

## Главные принципы

- Не использовать Android VPN API.
- Не создавать `tun0`.
- Не давать APK `INTERNET` без отдельного архитектурного решения; updater/subscription/control-plane HTTP сейчас принадлежат daemon.
- Не полагаться на Xposed/хуки как основную защиту.
- Всё сетевое состояние держать в root-слое.
- Любая ошибка запуска core должна быть диагностируема через `sing-box.log`.
- Конфиг должен быть машинно-валидируемым JSON, а не shell-sourced ini.

## Компоненты

### APK Controller

Пакет: `com.rknnovpn.panel`

Задачи:

- отображать статус подключения;
- импортировать nodes;
- управлять списком приложений;
- запускать проверки;
- показывать аудит;
- вызывать `privctl` через `su`.

APK намеренно не имеет:

- `INTERNET`;
- `ACCESS_NETWORK_STATE`;
- `VpnService`;
- VPN permissions.

### privctl

CLI-клиент, который:

- формирует JSON-RPC запрос;
- подключается к Unix socket `/data/adb/rknnovpn/run/daemon.sock`;
- печатает JSON-ответ;
- используется APK и пользователем из Termux/root shell.

Примеры:

```sh
su -c '/data/adb/rknnovpn/bin/privctl backend.status'
su -c '/data/adb/rknnovpn/bin/privctl backend.start'
su -c '/data/adb/rknnovpn/bin/privctl diagnostics.testNodes'
```

### privd

Root-демон. Основные обязанности:

- хранить и валидировать `/data/adb/rknnovpn/config/config.json`;
- принимать JSON-RPC команды;
- рендерить конфиг `sing-box`;
- запускать и останавливать `sing-box`;
- применять iptables/DNS/routing scripts;
- выполнять health checks;
- возвращать audit findings;
- скачивать подписки и обновления, потому что APK не имеет сети.

### IPC Contract

Transport остаётся JSON-RPC 2.0, но daemon payload внутри `result`/`error.data`
имеет typed envelope:

```json
{
  "ok": true,
  "result": {},
  "operation": null,
  "warnings": []
}
```

Ошибка сохраняет JSON-RPC code для совместимости, а daemon-level details лежат
в envelope:

```json
{
  "ok": false,
  "error": {
    "code": "CONFIG_APPLY_FAILED",
    "rpcCode": -32603,
    "message": "...",
    "details": {}
  },
  "operation": null,
  "warnings": []
}
```

APK `DaemonClient` должен читать typed envelope и не угадывать результат по
stdout, exit code или произвольным строкам.

### sing-box

Транспортный core:

- `tproxy` inbound;
- outbounds для VLESS/Trojan/VMess/Shadowsocks/SOCKS/Hysteria2/TUIC;
- `urltest` для выбора быстрого outbound;
- Clash API для delay-тестов;
- WireGuard build tag включён для будущей поддержки WireGuard outbound.

Сборка статическая. Workflow проверяет, что бинарник не требует системный dynamic loader.

## Каталоги

### Module directory

```text
/data/adb/modules/rknnovpn/
  module.prop
  customize.sh
  post-fs-data.sh
  service.sh
  uninstall.sh
  sepolicy.rule
  scripts/
    iptables.sh
    dns.sh
    routing.sh
    rescue_reset.sh
    lib/
      rknnovpn_env.sh
      rknnovpn_install.sh
      rknnovpn_installer_flow.sh
      rknnovpn_netstack.sh
      rknnovpn_iptables_rules.sh
  defaults/
    config.json
```

### Runtime data directory

```text
/data/adb/rknnovpn/
  bin/
    privd
    privctl
    sing-box
  config/
    config.json
    config.defaults.json
    rendered/
      singbox.json
  logs/
    privd.log
    sing-box.log
  run/
    daemon.sock
    privd.pid
    singbox.pid
  profiles/
  backup/
```

## Boot sequence

1. Magisk/KSU/APatch запускает `post-fs-data.sh`.
2. Скрипт создаёт каталоги, проверяет права, настраивает sysctl.
3. На `late_start service` запускается `service.sh`.
4. `service.sh` ждёт boot completed и стартует `privd`.
5. `privd` поднимает Unix socket.
6. APK подключается через `privctl`.
7. При команде `start` демон рендерит `singbox.json`, запускает `sing-box`, ждёт порт `10853`, применяет iptables и DNS.

Если `sing-box` завершается до открытия порта, `privd` возвращает ошибку с хвостом `/data/adb/rknnovpn/logs/sing-box.log`.

## Конфигурация

Основной файл:

```text
/data/adb/rknnovpn/config/profile.json
```

Ключевые секции:

- `schemaVersion`, `id`, `name` - typed profile identity and schema;
- `nodes`, `activeNodeId`, `subscriptions` - user proxy intent and subscription
  state owned by daemon;
- `routing`, `dns`, `health`, `sharing`, `inbounds` - user-visible policy;
- `runtime` - backend kind and runtime fallback policy;
- `extra` - explicit extension bucket for fields the daemon preserves.

Runtime projection:

```text
/data/adb/rknnovpn/config/config.json
```

- `proxy` - порты, GID core, fwmark;
- `node` - rendered active node mirror for the root runtime;
- `routing` - routing mode и domain/IP rules;
- `apps` - package whitelist/blacklist;
- `dns` - remote/direct DNS;
- `health` - URL проверки и интервалы;
- `rescue` - политика восстановления;
- `autostart`.

`panel.json` is not a supported storage path in v2. Fresh installs start from
`config.json`; daemon creates and then owns canonical `profile.json`.

APK-facing intent теперь принадлежит daemon `profile` contract. APK читает и
меняет только:

- `profile.get`
- `profile.apply`
- `profile.importNodes`
- `profile.setActiveNode`
- `subscription.preview`
- `subscription.refresh`

Legacy `config-get`, `config-set*`, `panel-*` и `subscription-fetch` не входят в
supported IPC contract и не рекламируются в `version.supported_methods` или
`privctl help`.

Daemon IPC contract фиксируется в `daemon/internal/ipc/contract_manifest.json`.
Он содержит method name, capability, mutating/async metadata, request/result
schema labels, error codes и operation stages. Daemon registration,
`version.supported_methods`, `doctor`, `privctl help` и APK compatibility checks
должны расходиться только через этот manifest, а не через ручные списки в разных
слоях.

Profile mutation response различает сохранение desired state и runtime apply:
`configSaved=true` означает, что persisted desired profile изменился, а
`runtimeApplied=true` ставится только когда запущенный runtime действительно
перезагружен/применён. Асинхронный reload/restart возвращает `runtimeApply:
accepted`, но не выставляет `runtimeApplied=true` до фактического завершения
operation. Save-only операции возвращают `runtimeApply: not_requested`, а
сохранение при остановленном runtime - `skipped_runtime_stopped`.

Каждая profile/subscription мутация возвращает typed `operation` внутри result и
в IPC envelope. Это transaction summary, а не строка для UI:

```json
{
  "type": "profile-apply",
  "action": "profile.apply",
  "status": "ok",
  "configSaved": true,
  "runtimeApplied": false,
  "runtimeApply": "accepted",
  "desiredGeneration": 8,
  "appliedGeneration": 7,
  "rollback": "not_needed",
  "stages": [
    {"name": "validate", "status": "ok"},
    {"name": "render", "status": "ok"},
    {"name": "persist-draft", "status": "ok"},
    {"name": "runtime-apply", "status": "accepted"},
    {"name": "verify", "status": "accepted"},
    {"name": "commit-generation", "status": "accepted"}
  ]
}
```

При ошибке после сохранения `status=saved_not_applied`, `configSaved=true`,
`runtimeApply=failed`, `desiredGeneration != appliedGeneration`, а `rollback`
показывает, была ли выполнена cleanup/reset ветка. APK должен показывать
пользователю именно этот typed результат, а не угадывать состояние по тексту
ошибки.

Runtime operation state хранится отдельно от user intent:
`<dataDir>/run/runtime_state.json` содержит desired/applied state, health,
compatibility, `activeOperation`, `lastOperation` и `updatedAt`. Это
observable snapshot текущего runtime owner, а не source of truth для профиля.
`profile.json` остаётся user intent, `config.json` остаётся root/runtime
projection, а operation generation/result фиксируются через runtime state и
`backend.status`.

APK projection: repository слой публикует отдельный informational notice для
typed config transaction outcomes (`skipped_runtime_stopped`, `failed` +
`rollback`) и для subscription refresh summary. UI показывает этот notice
отдельно от errors, чтобы saved-but-not-applied и partial-success состояния не
выглядели как обычный generic failure.

## Update/Install Safety

Daemon updater устанавливает module/APK только из verified update directory:
`module.zip`, `panel.apk`, `SHA256SUMS.txt` и локальный
`update-manifest.json` должны лежать в `<dataDir>/update` и проходить checksum
verification перед install. `update-install` не принимает произвольные пути к
артефактам. Module zip проходит staging/preflight validation до установки APK
и до остановки runtime; версия `module.prop` должна совпадать с verified
release version из manifest. Module hot-update обязан остановиться до замены
бинарников, если canonical `scripts/rescue_reset.sh update-clean` недоступен
или возвращает ошибку. Missing cleanup script не является совместимым
fallback-сценарием: лучше отказать в update, чем заменить root runtime поверх
неизвестных iptables/DNS/routing артефактов.

Install operation пишет observable state в
`<dataDir>/run/update-install-state.json`: generation, artifacts, текущий step,
step status/code/detail и флаги `apk_installed` / `module_installed`. Это
дисковый след операции поверх in-memory `activeOperation`: если daemon или
устройство падает в середине install, следующий daemon видит, на каком этапе
оборвалась операция, и не должен выдавать UI бездоказательное "успешно".
`backend.status.updateInstall` публикует этот state в typed IPC contract, а APK
маппит orphaned `running` / `failed` / `unknown` install state в явную ошибку
обновления.

### Nodes

Daemon хранит полный список nodes в `profile.json` (`nodes`). Каждый node
содержит:

- `id`;
- `name`;
- `protocol`;
- `server`;
- `port`;
- `link`;
- `outbound`;
- `group`;
- `source` (`MANUAL` или `SUBSCRIPTION`, provider metadata);
- latency/response test metadata.

`outbound` хранится как typed node field с явным raw outbound payload для
совместимости импортированных ссылок, а renderer демона переводит его в
`sing-box` outbound.

Subscription refresh работает только в своём source scope: ручные nodes не
считаются удалёнными подпиской, а stale/removed применяется только к nodes того
же provider. Stale nodes остаются видимыми в UI, но не участвуют в выборе
активного runtime node и массовых `diagnostics.testNodes` операциях.

Daemon валидирует `profile.nodes[].source`: `MANUAL` или `SUBSCRIPTION`.
Subscription nodes обязаны иметь `providerKey`; nodes без поддерживаемого
`source` считаются невалидным v2 состоянием и должны быть заменены текущим
canonical `profile.json`, а не молча мигрированы. Явный `stale=true` для manual
node запрещён, потому что stale/removed является подписочным состоянием, а не
свойством ручного узла.

Отдельно от nodes в `profile.subscriptions` хранится metadata подписок:
`providerKey`, `url`, optional display `name`, `lastFetchedAt`, node counts,
traffic quota/expiry из `subscription-userinfo` и `parseFailures`. Это не
runtime input для sing-box, а доменный источник правды для refresh/preview/apply
поведения. Nodes с `source.type=SUBSCRIPTION` должны ссылаться на provider через
тот же `providerKey`, а daemon валидирует subscription records до сохранения.
`subscription.preview` и `subscription.refresh` сначала строят canonical
`SubscriptionSource` (`url` + `providerKey`), затем fetch/parse/merge работают
только в этом provider scope.

APK node UI обязан показывать subscription domain отдельно от manual nodes:
summary по provider (`active`, `removed/stale`, parse failures) строится из
`profile.subscriptions` + `nodes[].source`, а stale nodes остаются selectable=false.
Это защищает UX от старой модели "подписка просто добавила обычные nodes".

## Renderer sing-box

Renderer находится в:

```text
daemon/internal/config/renderer.go
```

Логика:

1. Считать `profile.nodes` из daemon config projection.
2. Преобразовать каждый node в `NodeProfile`.
3. Сгенерировать отдельный outbound tag для каждого node.
4. Если node один, tag `proxy` указывает прямо на него.
5. Если nodes несколько, создаётся `urltest` outbound с tag `proxy`.
6. Route `final` указывает на `proxy`.
7. DNS-секция генерируется в новом формате `sing-box 1.12+`.

Пример концепции:

```json
{
  "outbounds": [
    { "type": "vless", "tag": "node-a", "...": "..." },
    { "type": "trojan", "tag": "node-b", "...": "..." },
    {
      "type": "urltest",
      "tag": "proxy",
      "outbounds": ["node-a", "node-b"],
      "url": "https://www.gstatic.com/generate_204"
    },
    { "type": "direct", "tag": "direct" }
  ],
  "route": {
    "final": "proxy"
  }
}
```

## iptables / routing

Главный режим перехвата:

- `OUTPUT` mangle помечает локальный трафик выбранных UID;
- `PREROUTING` mangle отправляет отмеченный трафик в TPROXY;
- policy routing доставляет отмеченные пакеты на local socket;
- loop-prevention делается через GID `23333`, чтобы трафик самого `sing-box` не зацикливался.

IPv4 и IPv6 должны быть зеркальны. ICMP/ICMPv6 не проксируются через TPROXY и должны идти напрямую.

## DNS

DNS работает через `sing-box` DNS и локальный inbound:

- classic DNS перехватывается на порт `10856`;
- `sing-box` резолвит remote/direct DNS;
- legacy DNS server format не используется;
- `independent_cache`, `address`, `address_resolver` не должны генерироваться.

Новый DNS format обязателен для `sing-box 1.12+` и будущего `1.14`.

## Health и audit

Health проверяет:

- жив ли `sing-box`;
- слушает ли `tproxy` port;
- применены ли iptables/routing rules;
- работает ли DNS;
- нет ли критичных ошибок core.

Audit превращает health и config состояние в findings, пригодные для UI.

## Node tests

Команда:

```sh
privctl diagnostics.testNodes
```

Возвращает:

- TCP connect time до server:port;
- URL delay через конкретный outbound tag через Clash API;
- ошибки TCP/URL.

Зачем две метрики:

- TCP connect показывает доступность endpoint.
- URL delay показывает реальную отзывчивость через профиль.

Для белых списков второго типа важен именно URL delay / response duration. Ping может быть низким, но реальный трафик может душиться ограничением скорости.

## Текущие ограничения

- Per-app multi-proxy ещё не завершён: сейчас все выбранные приложения идут через общий `proxy`/`urltest`.
- Раздача/VPN sharing пока не реализована.
- WireGuard build tag включён, но import/render WireGuard профилей ещё нужно добавить.
- AmneziaWG требует отдельной проверки runtime/core.
- Speed-throttle probe пока не реализован как отдельный mini-download test.

## Планируемая multi-proxy архитектура

Целевой вариант:

```text
package uid A -> outbound group A / urltest A
package uid B -> outbound group B / urltest B
package uid C -> direct
```

Для этого нужно:

1. Хранить mapping `package -> outboundTag` или `package -> group`.
2. Рендерить несколько `urltest`/`selector` outbounds.
3. Добавить route rules по UID.
4. Расширить UI приложений: выбор группы/сервера для приложения.
5. Добавить массовые тесты и сортировку по TCP/URL/response metrics.

## Сборка

`sing-box` резолвится из последнего GitHub release и собирается статически.

Активные build tags:

```text
with_quic,with_wireguard,with_utls,with_clash_api,badlinkname,tfogo_checklinkname0
```

Не включены без необходимости:

- `with_gvisor`;
- `with_dhcp`.

Это уменьшает размер бинарника, но сохраняет нужные текущие и ближайшие протоколы.

## Безопасность

- APK без сети.
- API `sing-box` должен быть доступен только локально/root-контролю.
- iptables защищает API-порт от non-root.
- Конфиги с credential хранятся с root-only правами.
- Ошибки запуска должны логироваться, а не скрываться за таймаутами.
