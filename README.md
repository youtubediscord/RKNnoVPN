# RKNnoVPN

Прозрачный прокси для rooted Android без `VpnService`, TUN-интерфейса и VPN-иконки.

Проект состоит из Magisk/KernelSU/APatch-модуля и Android-приложения-контроллера. Трафик выбранных приложений перехватывается на уровне ядра через `iptables TPROXY` и отправляется в `sing-box`.

## Что это даёт

- Нет Android VPN API: приложения не видят `TRANSPORT_VPN`.
- Нет `tun0` и системной VPN-иконки.
- APK не имеет `INTERNET`: подписки, update-check/download и сетевые диагностики выполняет root-демон через `daemonctl`.
- Можно проксировать только выбранные приложения, оставляя остальные напрямую.
- Сохранённые серверы рендерятся в `sing-box` как отдельные outbounds.
- Над несколькими серверами используется `urltest`, чтобы `sing-box` выбирал рабочий и быстрый outbound.
- Поддерживается `arm64-v8a` и `armeabi-v7a`.

## Как это работает

```text
Приложение Android
  -> iptables mangle / TPROXY
  -> sing-box tproxy inbound
  -> urltest / outbound
  -> прокси-сервер
```

APK вызывает `daemonctl` через `su`. `daemonctl` общается с root-демоном `daemon` по Unix socket. `daemon` хранит конфигурацию, рендерит `sing-box` config, запускает core, применяет iptables и выполняет health checks.

## Компоненты

| Компонент | Назначение |
|---|---|
| Magisk-модуль | Скрипты установки, boot service, SELinux policy, `daemon`, `daemonctl`, `sing-box` |
| `daemon` | root-демон: config, IPC, запуск core, iptables, DNS, health, update |
| `daemonctl` | CLI-клиент для ручной диагностики и IPC-команд |
| Android APK | Kotlin/Compose панель управления без собственного `INTERNET`; control-plane сеть принадлежит daemon |
| `sing-box` | transport core: tproxy inbound, outbounds, `urltest`, Clash API для delay-тестов |

## Быстрый старт

1. Скачайте свежий релиз:
   - `rknnovpn-vX.X.X-module.zip`
   - `rknnovpn-vX.X.X-panel.apk`

2. Прошейте module ZIP через Magisk Manager / KernelSU Manager / APatch.

3. Перезагрузите устройство.

4. Установите APK.

5. Добавьте сервер на вкладке `Серверы`:
   - вставкой ссылки;
   - QR-кодом;
   - подпиской.

6. На вкладке `Приложения` выберите приложения для проксирования.

7. Нажмите `Подключить`.

## Поддерживаемые протоколы

| Протокол | Статус |
|---|---|
| VLESS + Reality | основной сценарий |
| VLESS + TLS | поддерживается |
| Trojan | поддерживается |
| VMess | поддерживается |
| Shadowsocks / Shadowsocks 2022 | поддерживается |
| SOCKS4 / SOCKS5 upstream | поддерживается |
| Hysteria2 | поддерживается через `sing-box` |
| TUIC v5 | поддерживается через `sing-box` |
| WireGuard | запланирован; `sing-box` собирается с `with_wireguard` |
| AmneziaWG | отдельный будущий слой, не равен обычному WireGuard |

## Форматы импорта

Поддерживаются:

- `vless://`
- `vmess://`
- `trojan://`
- `ss://`
- `socks://`, `socks4://`, `socks4a://`, `socks5://`
- `hysteria2://`, `hy2://`
- `tuic://`
- `vpn://` Amnezia
- подписки с base64/plain URI list
- QR-коды

Пока не реализовано:

- Clash YAML import;
- sing-box JSON import;
- v2rayNG backup import;
- WireGuard `.conf` import.

## Выбор серверов и тесты

Все сохранённые nodes попадают в `sing-box` как outbounds. Если серверов несколько, `daemon` рендерит `urltest` outbound с тегом `proxy`, а маршрут по умолчанию указывает на него.

В UI есть `Проверить все`:

- TCP connect показывает время соединения до адреса сервера.
- URL delay показывает задержку реального запроса через конкретный outbound, если core уже запущен.

Для белых списков важно смотреть не только TCP ping. Низкий ping не гарантирует нормальную скорость: при ограничении со стороны ТСПУ пакет может отвечать быстро, но реальный HTTP/HTTPS response будет идти секунды. Поэтому главная метрика для авто-выбора - URL response / delay, а ping используется как вспомогательная диагностика.

## Режимы приложений

Текущая модель:

- один общий `urltest`/`proxy` outbound для выбранных приложений;
- список приложений через прокси или список приложений в обход;
- быстрые кнопки `Выбрать все` и `Снять весь выбор`.

Планируемая модель:

- несколько групп outbounds;
- привязка `package -> outbound/tag`;
- разные приложения через разные серверы или группы;
- отдельные `urltest` группы для браузеров, мессенджеров и т.п.

## Сборка

Требования:

- Go 1.22+ для `daemon`/`daemonctl`;
- Go 1.24.x для сборки актуального `sing-box`;
- JDK 17 + Android SDK для APK;
- Linux/macOS для кросс-сборки.

Команды:

```bash
make daemon    # daemon + daemonctl для arm64 + armeabi-v7a
make singbox   # статическая сборка sing-box для arm64 + armv7
make module    # Magisk module ZIP
make apk       # debug APK
make all
```

По умолчанию `make singbox` пытается взять последний релиз `sing-box` через GitHub CLI. Можно зафиксировать версию:

```bash
make singbox SINGBOX_VERSION=1.14.0-alpha.16
```

## CI/CD

Тег `v*` запускает GitHub Actions:

```bash
git tag v1.4.2
git push origin v1.4.2
```

Workflow:

- собирает `daemon`/`daemonctl` для `arm64` и `armv7`;
- резолвит актуальный release `sing-box`;
- собирает `sing-box` статически;
- собирает module ZIP;
- собирает APK;
- создаёт GitHub Release;
- обновляет `update.json`.

## Диагностика

Проверка состояния:

```sh
su -c '/data/adb/modules/rknnovpn/bin/daemonctl backend.status'
```

Запуск:

```sh
su -c '/data/adb/modules/rknnovpn/bin/daemonctl backend.start'
```

Логи core:

```sh
su -c 'cat /data/adb/modules/rknnovpn/logs/sing-box.log'
```

Логи daemon:

```sh
su -c '/data/adb/modules/rknnovpn/bin/daemonctl logs {"lines":100}'
```

Проверка всех nodes:

```sh
su -c '/data/adb/modules/rknnovpn/bin/daemonctl diagnostics.testNodes'
```

Если `sing-box` упал до открытия порта `10853`, ошибка в приложении должна содержать хвост `sing-box.log`.

## Что пока не готово

- VPN sharing / раздача как в NetProxy.
- Per-app multi-proxy с привязкой каждого приложения к отдельному outbound.
- WireGuard import и renderer.
- AmneziaWG runtime.
- Полный speed-throttle probe для диагностики второго типа белых списков.

## Лицензия

MIT

## Благодарности

- [sing-box](https://github.com/SagerNet/sing-box)
- [box_for_magisk](https://github.com/taamarin/box_for_magisk)
- [RKNHardering](https://github.com/xtclovver/RKNHardering)
