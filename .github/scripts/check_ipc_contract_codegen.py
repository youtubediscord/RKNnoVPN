#!/usr/bin/env python3
import argparse
import json
import pathlib
import sys


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
MANIFEST = REPO_ROOT / "daemon/internal/ipc/contract_manifest.json"
OUTPUT = REPO_ROOT / "app/app/src/main/kotlin/com/rknnovpn/panel/ipc/GeneratedDaemonContract.kt"


def render_contract(source: dict) -> str:
    version = int(source.get("version", 0))
    required = sorted(source.get("apkRequiredMethods", []))
    contracts = sorted(source.get("methods", []), key=lambda item: item.get("method", ""))
    methods = {item.get("method") for item in contracts}
    missing = [method for method in required if method not in methods]
    if missing:
        raise SystemExit(f"apkRequiredMethods contains undeclared method(s): {', '.join(missing)}")
    required_body = "\n".join(f'        "{method}",' for method in required)
    capabilities_body = "\n".join(
        f'        "{item.get("method", "")}" to "{item.get("capability", "")}",'
        for item in contracts
        if item.get("method")
    )
    mutating_body = "\n".join(
        f'        "{item.get("method", "")}",'
        for item in contracts
        if item.get("method") and item.get("mutating")
    )
    async_body = "\n".join(
        f'        "{item.get("method", "")}",'
        for item in contracts
        if item.get("method") and item.get("async")
    )
    return (
        "package com.rknnovpn.panel.ipc\n"
        "\n"
        "// Generated from daemon/internal/ipc/contract_manifest.json.\n"
        "// Run .github/scripts/check_ipc_contract_codegen.py --write after editing the IPC contract.\n"
        "internal object GeneratedDaemonContract {\n"
        f"    const val CONTRACT_VERSION: Int = {version}\n"
        "    val APK_REQUIRED_METHODS: Set<String> = setOf(\n"
        f"{required_body}\n"
        "    )\n"
        "    val METHOD_CAPABILITIES: Map<String, String> = mapOf(\n"
        f"{capabilities_body}\n"
        "    )\n"
        "    val MUTATING_METHODS: Set<String> = setOf(\n"
        f"{mutating_body}\n"
        "    )\n"
        "    val ASYNC_METHODS: Set<String> = setOf(\n"
        f"{async_body}\n"
        "    )\n"
        "}\n"
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--write", action="store_true", help="update the generated Kotlin source")
    args = parser.parse_args()

    source = json.loads(MANIFEST.read_text(encoding="utf-8"))
    rendered = render_contract(source)
    if args.write:
        OUTPUT.write_text(rendered, encoding="utf-8")
        return 0
    current = OUTPUT.read_text(encoding="utf-8") if OUTPUT.exists() else ""
    if current != rendered:
        print(f"{OUTPUT} is out of date; run {pathlib.Path(__file__).as_posix()} --write", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
