#!/usr/bin/env python3
"""Guard go-netops package boundaries using Go's production import graph.

go-netops is vendor and domain neutral: it must never import a consumer
module, and only the transport-facing packages may reach go-net-connector,
go-expect or the filesystem.
"""
import json
import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
MODULE = "github.com/gatework/go-netops"
CONSUMERS = ("github.com/gatework/go-vxlan", "github.com/gatework/go-wireless")
TRANSPORT = ("github.com/gatework/go-net-connector/", "github.com/gatework/go-expect")
IO = {"os", "os/exec", "io/fs", "net", "net/http", "database/sql", "path/filepath", "syscall"}
# Packages allowed to touch transport or the filesystem.
EDGE = {"store", "session"}
# Allowed internal dependencies: package -> local packages it may import.
LAYERS = {
    "": set(),
    "evidence": set(),
    "textscan": {"evidence"},
    "assess": {"evidence"},
    "drift": {"assess"},
    "change": {"drift", "evidence"},
    "apply": set(),
    "session": set(),
    "store": {"apply"},
}


def packages():
    env = {"GOWORK": "off", "GOFLAGS": "-mod=readonly"}
    import os
    text = subprocess.check_output(["go", "list", "-json", "./..."], cwd=ROOT, text=True, env={**os.environ, **env})
    decoder = json.JSONDecoder()
    while text.strip():
        package, end = decoder.raw_decode(text.lstrip())
        yield package
        text = text.lstrip()[end:]


def main():
    failures = []
    for package in packages():
        relative = package["ImportPath"].removeprefix(MODULE).removeprefix("/")
        if relative not in LAYERS:
            failures.append(f"undeclared package {relative}: add it to LAYERS")
            continue
        for dependency in package.get("Imports", []):
            if dependency.startswith(CONSUMERS):
                failures.append(f"{relative or 'root'} -> consumer module {dependency}")
            if dependency.startswith(MODULE + "/"):
                local = dependency.removeprefix(MODULE + "/")
                if local not in LAYERS[relative]:
                    failures.append(f"{relative or 'root'} -> {local} is not an allowed layer")
            if relative not in EDGE and (dependency in IO or dependency.startswith(TRANSPORT)):
                failures.append(f"pure package {relative or 'root'} -> {dependency}")
    if failures:
        sys.exit("architecture violations:\n" + "\n".join(sorted(set(failures))))
    print("PASS: go-netops stays domain neutral; only store and session reach transport or files")


if __name__ == "__main__":
    main()
