#!/usr/bin/env python3
"""Prepare a checksummed linux/amd64 cloud-shell build context."""

import argparse
import hashlib
import io
import json
from pathlib import Path
import shutil
import tarfile
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("go_output", type=Path, help="build-go-tools.sh output directory")
    parser.add_argument("--cli-version", required=True, help="Exact released CLI tag")
    args = parser.parse_args()
    here = Path(__file__).resolve().parent
    lock = json.loads((here / "runtime-lock.json").read_text())
    if args.cli_version != lock["cliVersion"]:
        parser.error("CLI version differs from the reviewed runtime lock")
    runtime = here / "runtime"
    if runtime.exists():
        shutil.rmtree(runtime)
    runtime.mkdir()
    expected_go_tools = {"kubectl", "stern", "k9s"}
    verified = {}
    for line in (args.go_output / "go-tools.sha256").read_text().splitlines():
        digest, name = line.split()
        if name not in expected_go_tools or name in verified:
            raise ValueError("Unexpected Go client checksum entry")
        data = (args.go_output / name).read_bytes()
        if hashlib.sha256(data).hexdigest() != digest:
            raise ValueError(f"Go client checksum mismatch: {name}")
        if digest != lock["goClientSHA256"].get(name):
            raise ValueError(f"Go client differs from the qualified executable: {name}")
        (runtime / name).write_bytes(data)
        verified[name] = digest
    if verified.keys() != expected_go_tools:
        raise ValueError("Go client output is incomplete")
    for item in lock["downloads"]:
        with urllib.request.urlopen(item["url"], timeout=120) as response:
            data = response.read()
        if hashlib.sha256(data).hexdigest() != item["downloadSHA256"]:
            raise ValueError(f"Release asset checksum mismatch: {item['name']}")
        if item.get("tarMember"):
            with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
                member = archive.getmember(item["tarMember"])
                if not member.isfile():
                    raise ValueError("Expected a regular executable in the archive")
                data = archive.extractfile(member).read()
        if hashlib.sha256(data).hexdigest() != item["binarySHA256"]:
            raise ValueError(f"Executable checksum mismatch: {item['name']}")
        name = item["name"]
        if name in verified or "/" in name or name.startswith("."):
            raise ValueError("Invalid or duplicate runtime executable")
        (runtime / name).write_bytes(data)
        verified[name] = item["binarySHA256"]
    for name in verified:
        (runtime / name).chmod(0o755)
    (runtime / "checksums.sha256").write_text(
        "".join(f"{digest}  {name}\n" for name, digest in sorted(verified.items()))
    )
    print(f"Prepared {len(verified)} verified clients in {runtime}")


if __name__ == "__main__":
    main()
