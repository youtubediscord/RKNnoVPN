# Google Pixel 9 Pro XL Device Lab

Эта папка закреплена за тестовым rooted Google Pixel 9 Pro XL. Идея простая:
сначала собирать read-only диагностику через ADB, потом запускать безопасный
smoke, и только после этого явно разрешать команды, которые могут менять
сетевое состояние телефона.

Локальные выводы тестов складывай в `googlepixel9proxl/artifacts/`. Эта папка
игнорируется git, потому что в ней могут быть device properties, логи,
диагностика `diagnostics.report`, package counts и другие приватные данные телефона.

## Подготовка

1. Включить USB debugging на телефоне.
2. Подключить телефон по USB.
3. Проверить, что ADB видит устройство:

```sh
adb devices -l
```

4. Если подключено несколько устройств, запомнить serial и передавать его как
   `ADB_SERIAL=...`.
5. Проверить root-доступ:

```sh
adb shell su -c id
```

## Первый безопасный прогон

Read-only сбор диагностики:

```sh
make lab-collect OUT_ROOT=googlepixel9proxl/artifacts
```

Если нужно указать serial:

```sh
make lab-collect ADB_SERIAL=<serial> OUT_ROOT=googlepixel9proxl/artifacts
```

Этот шаг не устанавливает APK/модуль, не перезагружает телефон, не удаляет
файлы и не меняет маршрутизацию. Он только читает состояние устройства и, если
`daemonctl` уже установлен, вызывает `status`, `self-check` и `diagnostics.report`.

## Safe Smoke

После read-only collect можно запустить безопасный smoke:

```sh
make lab-smoke OUT_ROOT=googlepixel9proxl/artifacts
```

С serial:

```sh
make lab-smoke ADB_SERIAL=<serial> OUT_ROOT=googlepixel9proxl/artifacts
```

По умолчанию smoke не делает `start`, `stop`, `reset`, install, uninstall или
reboot. Он проверяет только доступность `daemonctl`, `status`, `self-check`,
`diagnostics.report` и структуру diagnostics report JSON.

## Явно разрешенные mutating проверки

Запускать только когда телефон точно выделен под тесты.

Reset smoke:

```sh
tools/device_lab/smoke.sh --allow-reset --out-root googlepixel9proxl/artifacts
```

Start smoke с остановкой после проверки:

```sh
tools/device_lab/smoke.sh --allow-start --stop-after-start --out-root googlepixel9proxl/artifacts
```

Эти команды могут временно изменить сетевое состояние телефона. Если подключено
несколько устройств, добавить `--serial <serial>`.

## Правила безопасности

- Сначала всегда `make lab-collect`.
- Не запускать mutating флаги на личном основном телефоне без отдельного
  подтверждения.
- Не коммитить содержимое `googlepixel9proxl/artifacts/`.
- После любого `--allow-start` делать `--stop-after-start` или отдельный
  `daemonctl stop`, если цель теста не требует оставить routing включенным.
- При странном состоянии сначала собирать новый `diagnostics.report`, а не чинить руками.
