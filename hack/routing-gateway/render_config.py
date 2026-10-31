#!/usr/bin/env python3
"""Render the optional TCP-MD5 password without exposing it in a ConfigMap."""

import ipaddress
import json
import os
import pathlib
import subprocess
import sys


PLACEHOLDER = "__KUBE_DC_BGP_PASSWORD__"
ROUTER_ID_PLACEHOLDER = "__KUBE_DC_ROUTER_ID__"
EXPORT_MED_PLACEHOLDER = "__KUBE_DC_EXPORT_MED__"


def interface_ipv4(interface: str, purpose: str) -> str:
    try:
        result = subprocess.run(
            ["ip", "-j", "-4", "address", "show", "dev", interface],
            check=True,
            capture_output=True,
            text=True,
        )
        interfaces = json.loads(result.stdout)
        addresses = [
            item["local"]
            for entry in interfaces
            for item in entry.get("addr_info", [])
            if item.get("family") == "inet" and item.get("scope") == "global"
        ]
        if len(addresses) != 1:
            raise ValueError(f"expected one global IPv4 address, found {len(addresses)}")
        return str(ipaddress.IPv4Address(addresses[0]))
    except (OSError, subprocess.CalledProcessError, json.JSONDecodeError, KeyError, ValueError) as exc:
        raise RuntimeError(f"cannot derive {purpose} from {interface}: {exc}") from exc


def transit_router_id(interface: str) -> str:
    return interface_ipv4(interface, "BGP router ID")


def routing_link_address(interface: str) -> str:
    return interface_ipv4(interface, "routing-link address")


def export_med(routing_link_ip: str) -> str:
    raw_addresses = os.environ.get("ROUTING_LINK_IPS", "")
    try:
        addresses = [
            str(ipaddress.IPv4Address(value.strip()))
            for value in raw_addresses.split(",")
            if value.strip()
        ]
    except ipaddress.AddressValueError as exc:
        raise RuntimeError(f"ROUTING_LINK_IPS contains an invalid IPv4 address: {exc}") from exc
    if not addresses or len(addresses) != len(set(addresses)):
        raise RuntimeError("ROUTING_LINK_IPS must contain unique gateway IPv4 addresses")
    try:
        index = addresses.index(routing_link_ip)
    except ValueError as exc:
        raise RuntimeError(
            f"routing-link address {routing_link_ip} is absent from ROUTING_LINK_IPS"
        ) from exc
    return str(index * 100)


def main() -> int:
    if len(sys.argv) != 4:
        print("usage: render_config.py TEMPLATE OUTPUT PASSWORD_FILE", file=sys.stderr)
        return 2

    template_path, output_path, password_path = map(pathlib.Path, sys.argv[1:])
    config = template_path.read_text(encoding="utf-8")
    if ROUTER_ID_PLACEHOLDER in config or EXPORT_MED_PLACEHOLDER in config:
        try:
            router_id = transit_router_id(os.environ.get("TRANSIT_INTERFACE", "net2"))
        except RuntimeError as exc:
            print(str(exc), file=sys.stderr)
            return 1
        config = config.replace(ROUTER_ID_PLACEHOLDER, router_id)
        if EXPORT_MED_PLACEHOLDER in config:
            try:
                link_ip = routing_link_address(os.environ.get("ROUTING_LINK_INTERFACE", "net1"))
                config = config.replace(EXPORT_MED_PLACEHOLDER, export_med(link_ip))
            except RuntimeError as exc:
                print(str(exc), file=sys.stderr)
                return 1
    if PLACEHOLDER in config:
        try:
            password = password_path.read_bytes().decode("utf-8")
        except OSError:
            print("BGP authentication is configured but its Secret is unavailable", file=sys.stderr)
            return 1
        except UnicodeError:
            print("BGP authentication Secret has an invalid encoding", file=sys.stderr)
            return 1
        password = password.removesuffix("\n")
        if not 1 <= len(password) <= 80 or any(not 33 <= ord(char) <= 126 for char in password):
            print(
                "BGP authentication password must contain 1–80 printable ASCII characters without whitespace",
                file=sys.stderr,
            )
            return 1
        # FRR's LINE token retains quote and backslash bytes literally. Adding
        # JSON quotes changes the TCP-MD5 key. A single printable token cannot
        # inject another command or be normalized by FRR's whitespace splitting.
        config = config.replace(PLACEHOLDER, password)

    old_umask = os.umask(0o077)
    try:
        pathlib.Path(output_path).write_text(config, encoding="utf-8")
    finally:
        os.umask(old_umask)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
