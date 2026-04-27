#!/usr/bin/env python3
import argparse
import json
import sys
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description="Validate a PrivStack doctor JSON bundle.")
    parser.add_argument("path", type=Path)
    parser.add_argument("--strict-package-resolution", action="store_true")
    args = parser.parse_args()

    try:
        data = json.loads(args.path.read_text(encoding="utf-8"))
    except Exception as exc:
        print(f"doctor JSON is not parseable: {exc}", file=sys.stderr)
        return 1

    errors = []
    warnings = []

    summary = data.get("summary")
    if not isinstance(summary, dict):
        errors.append("missing summary object")
    else:
        if not summary.get("status"):
            errors.append("summary.status is empty")
        if "issueCount" not in summary:
            errors.append("summary.issueCount is missing")

    versions = data.get("versions")
    if not isinstance(versions, dict):
        errors.append("missing versions object")
    else:
        methods = versions.get("supported_methods") or []
        for method in ("doctor", "self-check", "backend.reset", "profile.get", "profile.apply"):
            if method not in methods:
                warnings.append(f"supported_methods does not advertise {method}")

    profile = data.get("profile")
    if profile is None and isinstance(summary, dict):
        profile = summary.get("profile")
    if not isinstance(profile, dict):
        warnings.append("missing profile summary object")
    else:
        if not profile.get("schemaVersion"):
            warnings.append("profile.schemaVersion is empty")
        if profile.get("desiredGeneration", 0) < profile.get("appliedGeneration", 0):
            warnings.append("profile desiredGeneration is older than appliedGeneration")

    package_resolution = data.get("package_resolution")
    if not isinstance(package_resolution, dict):
        message = "missing package_resolution object"
        if args.strict_package_resolution:
            errors.append(message)
        else:
            warnings.append(message)
    else:
        mode = package_resolution.get("mode", "")
        requested = package_resolution.get("requestedPackages") or []
        resolved_count = package_resolution.get("resolvedUidCount", 0)
        if mode in ("whitelist", "blacklist") and requested and resolved_count == 0:
            warnings.append("per-app routing selected packages resolve to zero UIDs")
        if package_resolution.get("warnings"):
            warnings.extend(str(item) for item in package_resolution["warnings"])

    conflicts = data.get("port_conflicts") or []
    if conflicts:
        warnings.append(f"configured local port conflicts: {len(conflicts)}")

    leftovers = data.get("leftovers") or []
    if leftovers:
        warnings.append(f"network leftovers reported: {len(leftovers)}")

    for warning in warnings:
        print(f"WARN: {warning}")
    for error in errors:
        print(f"ERROR: {error}", file=sys.stderr)
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
