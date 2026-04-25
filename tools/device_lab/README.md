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
