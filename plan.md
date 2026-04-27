# План развития PrivStack / RKNnoVPN

Документ описывает актуальный roadmap проекта после перехода на `sing-box`, `urltest` outbounds и root-only TPROXY архитектуру.

## Цель

Сделать прозрачный прокси-стек для rooted Android, который:

- не использует Android VPN API;
- не создаёт TUN-интерфейс;
- не показывает VPN-иконку;
- умеет маршрутизировать приложения через разные профили;
- умеет автоматически выбирать быстрый профиль;
- даёт понятную диагностику качества маршрута;
- поддерживает будущий WireGuard/WG импорт;
- остаётся управляемым через APK без сетевых разрешений.

## Текущее состояние

Уже есть:

- Magisk/KernelSU/APatch module;
- `privd` root daemon;
- `privctl` JSON-RPC CLI;
- Android APK на Kotlin/Compose;
- импорт VLESS/VMess/Trojan/Shadowsocks/SOCKS/Hysteria2/TUIC/Amnezia `vpn://`;
- хранение списка nodes в `panel.nodes`;
- рендер нескольких nodes как `sing-box` outbounds;
- `urltest` outbound с тегом `proxy`;
- `diagnostics.testNodes` TCP/URL RPC;
- APK без `INTERNET`;
- сборка `sing-box` latest release;
- статическая сборка под `arm64` и `armv7`;
- build tag `with_wireguard` включён для будущего WG слоя.

## Ближайшие принципы

1. Не возвращаться к модели “один active node = один proxy”.
2. Все nodes должны жить в `sing-box` одновременно.
3. Выбор маршрута должен происходить через `urltest`, `selector` и route rules.
4. Ping не считать главным показателем качества.
5. Для белых списков второго типа измерять response duration / throughput.
6. Не включать тяжёлые build tags без понятного runtime-смысла.
7. Не скрывать ошибки core за таймаутами: лог должен быть виден пользователю.

## Актуальный план стабилизации v1.6.4+

Этот блок важнее старого feature-roadmap ниже. Новые функции нельзя делать,
пока data-plane не стал предсказуемым и диагностируемым.

### M0. Emergency stabilization

Цель: не ломать сеть и не требовать reboot, если core уже поднялся, а мягкая
диагностика DNS/egress не прошла.

Сделано/закрывается в v1.6.4:

- hard readiness отделён от soft DNS/egress diagnostics;
- DNS timeout переводит runtime в degraded, а не в hard error;
- APK показывает `Подключено / degraded`, если core+routing живы;
- reset возвращает структурный `ResetReport`;
- `doctor` и `logs` доступны из UI;
- update/version gate различает repair-команды и команды запуска core.
- health probe-set больше не завязан на один `www.gstatic.com`;
- отчёт диагностики можно скопировать одной кнопкой из Settings.

Acceptance:

- TCP до node есть, core запущен, routing готов -> UI не показывает большую
  красную `Ошибка` только из-за DNS/URL probe;
- `diagnostics.testNodes` сохраняет TCP-direct диагностику даже если tunnel/url недоступен;
- `self-check` возвращает краткий health/privacy/compatibility summary без
  полного diagnostic bundle;
- self-check/doctor summary включает компактные `compatibility` и `runtime`
  поля: версии daemon/module/schema/control protocol, release status и последнюю
  runtime stage summary; compatibility summary также показывает current release
  version и результат `sing-box check`;
- APK IPC client имеет typed `selfCheck()` только через canonical `self-check`, чтобы UI
  мог получать короткий repair summary без полного doctor bundle;
- `StatusRepository` прокидывает typed `selfCheck()` наверх, чтобы будущий
  Settings/Audit экран не зависел от низкоуровневого IPC клиента;
- `backend.reset` не блокируется из-за version mismatch или отсутствующего
  `sing-box`;
- кривой module update zip отбрасывается до остановки рабочего runtime.

### M1. Transactional runtime

Разбить старт на typed стадии:

```text
render config
sing-box check
spawn sing-box
wait tproxy/dns/api ports
apply routing
apply iptables
apply DNS
probe runtime
commit state
```

Каждая стадия должна возвращать:

```text
layer
code
hard
userMessage
debug
rollbackApplied
```

Acceptance:

- rollback чистит только применённые стадии;
- `CoreManager.Start()` больше не является одним большим error string;
- старт ждёт `tproxy`, `dns` и опциональный `api` listener до применения правил;
- hot-swap ждёт тот же набор runtime listeners (`tproxy`, `dns`, optional
  `api`), а не только TPROXY;
- successful start закрывает и `lastStartReport`, и `lastRuntimeReport` как
  `ok`, чтобы status/doctor не показывали зависшую стадию `running`;
- до первого health result runtime snapshot может считать hard readiness
  зелёной по successful stage report, не придумывая soft DNS/egress успех;
- UI может показать, где именно остановился запуск:
  `CONFIG_CHECKED`, `CORE_SPAWNED`, `CORE_LISTENING`, `RULES_APPLIED`,
  `DNS_APPLIED`, `OUTBOUND_CHECKED`, `DEGRADED`.

### M2. Netstack ownership

Собрать `iptables`, DNS redirect и policy routing в единый контракт:

```text
netstack apply
netstack cleanup
netstack verify
netstack report
```

Acceptance:

- cleanup удаляет только PrivStack-owned artifacts;
- проверка остатков смотрит IPv4/IPv6 и raw/mangle/nat/filter;
- `netstack verify` вызывает `iptables.sh status`/`dns.sh status` и
  отличает `NETSTACK_VERIFY_FAILED` от apply/cleanup ошибок;
- `iptables.sh status` проверяет не только наличие chains/hooks/routes, но и
  DROP protection rules для TPROXY/DNS/API/SOCKS/HTTP local listener ports;
- reset с cleanup leftovers возвращает `clean_with_warnings`, а не hard
  failure; hard `partial` остаётся для реальных ошибок шагов;
- DNS остаётся network-layer redirect, без изменения Android system DNS;
- local API/DNS/TProxy/SOCKS/HTTP helper ports закрыты от non-root/non-core UID
  и участвуют в privacy/audit проверках.

### M3. Compatibility and release safety

Строго разделить:

- APK version;
- daemon version;
- module version;
- control protocol version;
- supported methods;
- `sing-box` availability.

Acceptance:

- APK не вызывает неподдерживаемый RPC;
- start/restart требуют working `sing-box`;
- reset/logs/doctor/node TCP diagnostics остаются доступны для ремонта;
- update installer принимает только canonical verified update dir, требует
  `update-manifest.json` + checksums и проверяет module zip до APK install /
  runtime downtime.
- `version` отдаёт `schema_version`, `panel_min_version`, `capabilities`,
  `supported_methods`, module/core/daemon metadata.
- APK gate проверяет schema/capabilities перед не-repair RPC, а repair-команды
  остаются доступными для восстановления.
- APK required-methods перечисляет canonical RPC contract, включая
  `profile.get`, `profile.apply`, `profile.importNodes`,
  `profile.setActiveNode`, `subscription.preview`, `subscription.refresh`,
  `backend.reset`, `backend.stop`, `diagnostics.testNodes`,
  update/logs/doctor/self-check методы.
- Legacy IPC aliases удалены из daemon registration, privctl help, APK client и
  doctor supported methods.
- `diagnostics.testNodes` сохраняет `url_error_class`, чтобы UI показывал
  классифицированную причину отказа, а не только сырой error string.
- Compatibility check больше не принимает альтернативные legacy entrypoints.

### M4. Privacy invariants

Добавить self-test/audit для поверхности RKNHardering:

```text
no VpnService
no TRANSPORT_VPN
no tun0/wg0/tap0/ppp/ipsec interface
no system proxy
no loopback DNS in LinkProperties
no public SOCKS/HTTP listener
no public Xray/Clash API
protected/direct-only app set is active
```

Часть self-test уже находится в `audit`/`doctor`: API/helper listeners,
system proxy, loopback DNS в LinkProperties, VPN-like interfaces,
per-app whitelist/off defaults и
diagnostic privacy surface. RKNHardering и YourVPNDead входят в built-in
direct-only/self-test protected package set.
Основной `audit` RPC тоже выдаёт отдельный `LOOPBACK_DNS_VISIBLE` finding,
если `dumpsys connectivity` показывает loopback DNS в LinkProperties.
HTTP/SOCKS helper inbound теперь считается audit finding даже на localhost;
LAN exposure остаётся отдельной более конкретной причиной.
Diagnostic privacy payload отдаёт `protected_packages.self_test`, чтобы
проверяемый direct-only набор был виден в отчёте.
Audit/doctor проверяют configured helper/API ports и common proxy ports
10808/10809/9090 на stale localhost listeners.
Audit helper для local port protection теперь учитывает отсутствие IPv6 mangle
как допустимый Android-вариант и проверяет TCP/UDP protection отдельно для
TPROXY/DNS.
VPN-like interface detection теперь смотрит имя интерфейса из `ip link`, а не
только фиксированные `tun0/wg0/tap0` имена.
Doctor bundle теперь разделяет cleanup leftovers (`netstack`) и runtime
apply/status verification (`netstack_runtime`), чтобы stopped runtime не
выглядел как failed rules.
Runtime netstack verification failure теперь попадает и в doctor summary, а не
только в сырой payload.
Daemon advertises `netstack.runtime.verify.v1`, чтобы APK/diagnostics могли
отличать runtime apply/status checks от cleanup verification.
Daemon, module defaults и APK model defaults выровнены на whitelist/per-app
режим, без proxy-all как implicit empty-state.

Не делать:

- Xposed hooks;
- PackageManager masking;
- root hiding;
- подмену Android API.

### M5. UX diagnostics

Один экран должен отвечать на вопрос `что сломалось`:

- core binary/config;
- listener ports;
- iptables/routing;
- DNS listener;
- proxy DNS;
- outbound URL;
- selected node;
- APK/module mismatch.
- summary в diagnostic bundle сразу показывает health/privacy/compatibility
  issues, TCP-only diagnostics и необходимость reboot после cleanup leftovers.
- diagnostic/status payload показывает `active_node_mode` (`auto_selector`,
  `manual`, `single_node`, `manual_missing`), а Dashboard отображает auto/manual
  понятным текстом.
- UI formatter локализует `NETSTACK_VERIFY_FAILED` и
  `NETSTACK_CLEANUP_FAILED`, чтобы typed runtime errors не выпадали английским
  debug-сообщением.
- Reset summary показывает warnings даже когда отдельные шаги формально `ok`,
  чтобы `clean_with_warnings` не выглядел пустым результатом.
- Кнопка `Скопировать отчёт` всегда запрашивает свежий diagnostic bundle и не
  подменяет его ранее открытыми runtime logs.

Отчёт должен быть redacted по умолчанию.

Текущий redaction contract: credentials/keys/passwords/UUID скрываются,
включая WireGuard/Amnezia `pre_shared_key`/`preshared_key`/`psk`, а
server/port/SNI остаются видимыми для диагностики маршрута.

### M6. Feature roadmap after stabilization

Только после M0-M5:

1. selector + manual override; начальный slice сделан: `proxy` рендерится как
   selector, `auto` остаётся `urltest`, active node становится selector default.
   UI-slice тоже сделан: список серверов умеет переключать `Авто` /
   `Вручную`, где `Авто` очищает `active_node_id`, а выбор карточки задаёт
   manual override. APK config bridge сохраняет пустой `active_node_id` как
   auto-mode для `panel.nodes`, а не подставляет первый сервер после refresh.
2. speed/throughput probes; начальный slice сделан: прозрачная URL-проба
   возвращает `responseBytes/throughputBps`, если probe URL отдаёт тело, а
   Clash delay остаётся честным latency-only. UI-slice тоже сделан: список
   серверов показывает скорость и умеет сортировать по throughput.
3. per-app groups; начальный renderer-slice сделан: `panel.nodes[].group`
   протягивается в daemon profile и создаёт group selector/urltest outbounds
   без изменения дефолтного маршрута. Следующий slice тоже сделан на уровне
   config/renderer: `apps.app_groups` задаёт package -> node group и рендерит
   `package_name` route rules на group selector. UI-slice тоже сделан:
   App picker может назначать package -> node group.
4. WireGuard outbound import/render without kernel WG interface; начальный slice
   сделан: WireGuard config text/encoded URI импортируется как node, daemon
   рендерит sing-box `wireguard` outbound без `interface_name`/kernel WG.
5. hotspot/sharing mode; начальный control-plane slice сделан:
    `sharing.enabled/interfaces` протянуты в daemon/APK config, а
   daemon-owned netstack apply добавляет tethering TPROXY rules только при
   `SHARING_MODE=hotspot`. UI-slice тоже сделан в Settings.

## Этап 1. Stabilize current multi-outbound layer

Статус: частично сделано.

Задачи:

- Убедиться, что `panel.nodes -> outbounds[] -> urltest proxy` работает на реальном устройстве.
- Проверить VLESS Reality, Trojan, Shadowsocks, SOCKS5, Hysteria2, TUIC.
- Проверить DNS на `sing-box 1.14.x`.
- Проверить, что `diagnostics.testNodes` возвращает:
  - TCP connect ms;
  - URL delay ms;
  - status/error.
- Исправить UI для пустого списка серверов.
- Убедиться, что `privctl diagnostics.testNodes` не требует running core для TCP-теста, но корректно сообщает, что URL delay невозможен без Clash API.

Acceptance:

- `privctl backend.start` запускает core без DNS fatal.
- `sing-box.log` при ошибках содержит конкретный fatal.
- `privctl diagnostics.testNodes` показывает результаты по всем сохранённым nodes.
- UI не даёт запускать core без node.

## Этап 2. Selector + manual override

Цель: дать пользователю ручной выбор, но оставить auto-select.

Renderer должен создавать:

```text
node-1
node-2
node-3
auto-urltest
manual-selector
proxy -> selector/urltest
```

Варианты:

- `proxy = urltest` по умолчанию;
- `proxy = selector`, где selector может выбрать `auto` или конкретный node.

UI:

- “Авто”;
- “Ручной сервер”;
- показать текущий выбранный outbound;
- показать последний URL delay.

Acceptance:

- пользователь может выбрать конкретный сервер без удаления auto mode;
- switching не требует полного переимпорта nodes;
- `sing-box` не перезапускается без необходимости.

## Этап 3. Per-app multi-proxy

Цель: разные приложения через разные outbounds/groups.

Текущая модель:

```text
selected apps -> proxy
```

Целевая модель:

```text
Telegram UID -> group-social-auto
Chrome UID   -> group-browser-auto
Game UID     -> node-low-latency
Bank UID     -> direct
```

Изменения в данных:

- добавить mapping:

```json
{
  "app_routing": {
    "org.telegram.messenger": "group-social",
    "com.android.chrome": "group-browser",
    "ru.bank.app": "direct"
  }
}
```

Или компактнее:

```json
{
  "groups": {
    "browser": ["node-a", "node-b"],
    "messenger": ["node-c", "node-d"]
  },
  "app_groups": {
    "com.android.chrome": "browser",
    "org.telegram.messenger": "messenger"
  }
}
```

Renderer:

- создать outbounds для nodes;
- создать `urltest` для каждой группы;
- создать route rules по UID/package;
- `final` оставить на общий `proxy` или `direct` в зависимости от режима.

iptables:

- текущий UID whitelist может остаться как coarse filter;
- детальный выбор outbound лучше делать внутри `sing-box` route rules, если UID/package match поддержан в нужной форме;
- если UID routing в sing-box окажется недостаточным, потребуется несколько TPROXY inbound/ports и iptables маркировка по группам.

Риск:

- несколько TPROXY inbound усложняют iptables, но дают жёсткую привязку UID -> inbound -> outbound.
- один TPROXY inbound + sing-box route проще, но надо проверить поддержку нужных UID/package matches.

Предпочтительный порядок:

1. Сначала попробовать один TPROXY inbound + sing-box route rules.
2. Если не хватает контроля, перейти к нескольким TPROXY inbound ports.

## Этап 4. Диагностика белых списков и speed throttling

Проблема:

Низкий ping не означает хороший маршрут. При втором типе белых списков IP не заблокирован, но трафик ограничен по скорости. TCP connect может быть 30 ms, а реальный ответ будет идти 10 секунд.

Нужные метрики:

- TCP connect time;
- TLS/transport handshake time;
- URL TTFB;
- full response time;
- small download throughput;
- jitter;
- error class:
  - DNS fail;
  - TCP timeout;
  - TLS fail;
  - HTTP timeout;
  - slow body;
  - reset/refused.

Минимальный тест:

1. TCP connect до endpoint.
2. URL delay через outbound.
3. Загрузка небольшого файла 256KB/512KB через outbound.
4. Расчёт KB/s и duration.

UI:

```text
TCP       34 ms
URL       180 ms
TTFB      210 ms
Load      430 KB/s
Status    OK
```

Сортировки:

- по URL delay;
- по TCP connect;
- по throughput;
- по стабильности;
- “лучший для белых списков”.

Acceptance:

- пользователь видит разницу между “пингуется” и “реально работает быстро”;
- auto-select может учитывать не только ping, но и response/throughput.

## Этап 5. WireGuard

Сборка уже включает `with_wireguard`.

Нужно добавить:

- `Protocol.WIREGUARD`;
- импорт WireGuard `.conf`;
- импорт QR;
- хранение:
  - private key;
  - peer public key;
  - preshared key;
  - endpoint;
  - local addresses;
  - allowed IPs;
  - MTU;
  - reserved bytes, если нужно;
- renderer `type: wireguard`;
- участие WG nodes в `urltest`;
- `diagnostics.testNodes` для WG endpoint и URL delay.

Важно:

- обычный WireGuard не равен AmneziaWG;
- AmneziaWG может потребовать отдельный core/adapter, если `sing-box` не поддержит нужные поля.

Acceptance:

- обычный WG `.conf` импортируется и запускается;
- WG outbound участвует в auto selection;
- UI показывает WG как обычный node.

## Этап 6. Раздача / VPN sharing

Текущее состояние:

- forwarding включается;
- полноценное proxy sharing как NetProxy не готово.

Нужно:

- понять интерфейсы tethering/hotspot;
- маркировать forwarded traffic в PREROUTING;
- не ломать private/LAN bypass;
- проверить IPv4/IPv6;
- проверить DNS клиентов hotspot;
- добавить отдельный режим “проксировать раздачу”.

Возможные схемы:

1. Forwarded traffic -> общий TPROXY inbound -> общий `proxy/urltest`.
2. Forwarded traffic -> отдельный TPROXY inbound -> отдельный outbound group.
3. По клиентам/интерфейсам делать разные groups.

Acceptance:

- клиент на hotspot получает интернет через proxy;
- DNS клиента не течёт напрямую;
- локальная сеть не ломается;
- выключение режима чистит iptables.

## Этап 7. UX и ошибки

Нужно улучшить:

- пустой active node;
- видимые причины падения core;
- диагностику установки module/APK mismatch;
- предупреждение “у вас APK vX, module vY”;
- встроенный просмотр `sing-box.log`;
- встроенный просмотр generated `singbox.json`;
- кнопка “скопировать диагностический отчёт”.

## Этап 8. Релизы

Политика:

- patch-релизы для быстрых bugfix;
- minor-релизы для архитектурных слоёв;
- не выпускать релиз, если tag workflow красный;
- `update.json` должен указывать на последний stable release.

Workflow должен:

- резолвить последний release `sing-box`;
- собирать статически;
- проверять отсутствие dynamic loader;
- собирать APK;
- собирать module ZIP;
- публиковать release;
- обновлять `update.json`;
- делать rebase перед push `update.json`, чтобы избежать гонок.

## Приоритеты

1. Стабилизировать запуск `v1.4.x` на устройстве.
2. Проверить `urltest` на реальных nodes.
3. Сделать нормальный speed-throttle test.
4. Добавить selector/manual override.
5. Сделать per-app groups.
6. Добавить WireGuard import/render.
7. Добавить sharing/hotspot mode.
