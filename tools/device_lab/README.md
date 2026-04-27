# Device Lab

Safe ADB helpers for testing PrivStack on a rooted Android lab device.

The default commands are intentionally read-only. They collect diagnostics and
run daemon RPC checks without installing packages, rebooting, deleting files, or
changing routing state. Mutating checks require explicit flags.

## Usage

Connect exactly one Android device with USB debugging enabled, then run:

```sh
make lab-collect
make lab-smoke
```

When multiple devices are connected, pass a serial:

```sh
make lab-collect ADB_SERIAL=0123456789abcdef
make lab-smoke ADB_SERIAL=0123456789abcdef
```

Artifacts are written under `lab-artifacts/device_lab/` and are ignored by git.
Treat them as private: doctor output is redacted by the daemon, but device
properties, installed module paths, package counts, and logs can still identify
the test phone.

## Mutating Smoke Checks

The smoke script does not start, stop, reset, install, reboot, or remove
anything by default. Enable these only on a dedicated lab phone:

```sh
tools/device_lab/smoke.sh --allow-reset
tools/device_lab/smoke.sh --allow-start --stop-after-start
```

`--allow-start` applies the current PrivStack routing config on the device.
`--allow-reset` invokes the daemon reset RPC. Both can affect connectivity while
the test is running.

## Lineage/QEMU Emulator

PrivStack can also be tested against the QEMU LineageOS images from
`SamuraiArtem/android-lineage-emulator` or its upstream
`jqssun/android-lineage-qemu`. This is useful for fast root/runtime regressions,
but it does not replace real vendor phones because the emulator is ARM64 and its
kernel/network stack is cleaner than many production devices.

Start the extracted VM yourself, or through the helper:

```sh
tools/device_lab/lineage_emulator.sh start --vm-dir /path/to/android-lineage-emulator
```

Some Lineage/QEMU images keep ADB authorization behind a GRUB environment flag.
The helper can enable the image-supported `androidboot.insecure_adb=1` flag in
`vda.qcow2` before the VM starts:

```sh
sudo tools/device_lab/lineage_emulator.sh prepare-insecure-adb --vm-dir /path/to/android-lineage-emulator
```

Then connect and run the same lab checks through `localhost:5555`:

```sh
make lab-emulator-connect
make lab-emulator-collect
make lab-emulator-smoke
```

The helper waits up to 10 minutes for `sys.boot_completed=1`; the first boot on
TCG can spend several minutes formatting `/data` and decompressing APEX files.
It also verifies that `adb shell` works, not just that `adb devices` reports
`device`. If the transport is online but shell closes, run:

```sh
tools/device_lab/lineage_emulator.sh doctor --serial localhost:5555
```

An emulator with a broken shell service is not useful for PrivStack smoke tests,
even when the Android UI has booted.

To install locally built artifacts on a Magisk-enabled emulator:

```sh
make all VERSION=v1.7.13
make lab-emulator-install VERSION=v1.7.13
```

The install target pushes the APK and Magisk module ZIP, runs
`magisk --install-module`, and reboots the emulator. It is intentionally a
separate mutating target. After boot, run:

```sh
make lab-emulator-smoke-start
```

For a non-default adb binary or serial:

```sh
make lab-emulator-smoke ADB=/mnt/d/bin/platform-tools/adb.exe ADB_SERIAL=localhost:5555
```
