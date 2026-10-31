#!/usr/bin/env python3
"""Small FRR/nftables Prometheus exporter for one Project routing gateway."""

from __future__ import annotations

import ipaddress
import json
import os
import pathlib
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


LISTEN_ADDRESS = os.getenv("METRICS_LISTEN_ADDRESS", "127.0.0.1")
LISTEN_PORT = int(os.getenv("METRICS_LISTEN_PORT", "9100"))
BASE_LABELS = {
    "organization": os.getenv("ORGANIZATION", ""),
    "project": os.getenv("PROJECT", ""),
    "routed_network": os.getenv("ROUTED_NETWORK", ""),
    "gateway_replica": os.getenv("GATEWAY_REPLICA", os.getenv("HOSTNAME", "")),
}
EXPECTED_PEERS = {
    peer.strip() for peer in os.getenv("EXPECTED_PEERS", "").split(",") if peer.strip()
}
ALLOWED_IMPORTS = {
    ipaddress.ip_network(prefix.strip(), strict=False)
    for prefix in os.getenv("ALLOWED_IMPORT_PREFIXES", "").split(",")
    if prefix.strip()
}
ALLOWED_EXPORTS = {
    str(ipaddress.ip_network(prefix.strip(), strict=False))
    for prefix in os.getenv("ALLOWED_EXPORT_PREFIXES", "").split(",")
    if prefix.strip()
}
IMPORT_MATCH_MODE = os.getenv("IMPORT_MATCH_MODE", "exact-or-more-specific")
IMPORT_MIN_PREFIX_LENGTH = int(os.getenv("IMPORT_MIN_PREFIX_LENGTH", "0") or 0)
IMPORT_MAX_PREFIX_LENGTH = int(os.getenv("IMPORT_MAX_PREFIX_LENGTH", "32") or 32)
MAX_PREFIXES = max(1, int(os.getenv("MAX_PREFIXES", "100") or 100))
COLLECT_INTERVAL_SECONDS = max(
    1.0, float(os.getenv("COLLECT_INTERVAL_SECONDS", "2") or 2)
)
NFT_COUNTERS_FILE = pathlib.Path(
    os.getenv("NFT_COUNTERS_FILE", "/run/frr/nft-counters.json")
)
NFT_COUNTERS_MAX_AGE_SECONDS = max(
    1.0, float(os.getenv("NFT_COUNTERS_MAX_AGE_SECONDS", "15") or 15)
)

state_lock = threading.Lock()
last_peer_states: dict[str, str] = {}
peer_flaps: dict[str, int] = {}
seen_policy_violations: set[tuple[str, str, str]] = set()
snapshot_lock = threading.Lock()
last_snapshot = ""
last_healthy = False
last_live = False


def command_json(argv: list[str]) -> Any:
    completed = subprocess.run(
        argv,
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        text=True,
        timeout=3,
    )
    return json.loads(completed.stdout)


def find_peer_map(value: Any) -> dict[str, Any]:
    if isinstance(value, dict):
        peers = value.get("peers")
        if isinstance(peers, dict):
            return peers
        for child in value.values():
            found = find_peer_map(child)
            if found:
                return found
    elif isinstance(value, list):
        for child in value:
            found = find_peer_map(child)
            if found:
                return found
    return {}


def route_prefixes(value: Any) -> set[str]:
    """Extract canonical IPv4 prefixes from FRR route-table JSON."""
    found: set[str] = set()
    if isinstance(value, dict):
        for key, child in value.items():
            candidates = [key]
            if key in {"prefix", "network"} and isinstance(child, str):
                candidates.append(child)
            for candidate in candidates:
                if not isinstance(candidate, str) or "/" not in candidate:
                    continue
                try:
                    network = ipaddress.ip_network(candidate, strict=False)
                    if network.version == 4:
                        found.add(str(network))
                except ValueError:
                    pass
            found.update(route_prefixes(child))
    elif isinstance(value, list):
        for child in value:
            found.update(route_prefixes(child))
    return found


def as_loop_filtered_prefixes(value: Any) -> set[str]:
    """Find routes FRR must reject because every path contains the local AS."""
    if not isinstance(value, dict):
        return set()
    try:
        local_as = int(value.get("localAS", 0) or 0)
    except (TypeError, ValueError):
        local_as = 0
    if local_as <= 0:
        return set()

    def path_values(item: Any) -> list[str]:
        paths: list[str] = []
        if isinstance(item, dict):
            for key, child in item.items():
                if key == "path" and isinstance(child, str):
                    paths.append(child)
                else:
                    paths.extend(path_values(child))
        elif isinstance(item, list):
            for child in item:
                paths.extend(path_values(child))
        return paths

    found: set[str] = set()
    for table_name in ("receivedRoutes", "routes"):
        table = value.get(table_name)
        if not isinstance(table, dict):
            continue
        for prefix, details in table.items():
            try:
                network = ipaddress.ip_network(prefix, strict=False)
            except (TypeError, ValueError):
                continue
            paths = path_values(details)
            if network.version == 4 and paths and all(
                str(local_as) in path.split() for path in paths
            ):
                found.add(str(network))
    return found


def prefix_allowed(prefix: str) -> bool:
    try:
        route = ipaddress.ip_network(prefix, strict=False)
    except ValueError:
        return False
    if route.version != 4 or route.prefixlen == 0:
        return False
    for allowed in ALLOWED_IMPORTS:
        if IMPORT_MATCH_MODE == "exact":
            if route == allowed:
                return True
            continue
        minimum = max(allowed.prefixlen, IMPORT_MIN_PREFIX_LENGTH)
        if route.subnet_of(allowed) and minimum <= route.prefixlen <= IMPORT_MAX_PREFIX_LENGTH:
            return True
    return False


def peer_route_table(peer: str, table: str) -> tuple[set[str], bool]:
    try:
        value = command_json(["vtysh", "-c", f"show bgp ipv4 unicast neighbors {peer} {table} json"])
        return route_prefixes(value), True
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
        return set(), False


def peer_received_route_table(peer: str) -> tuple[set[str], set[str], bool]:
    try:
        value = command_json(
            [
                "vtysh",
                "-c",
                f"show bgp ipv4 unicast neighbors {peer} received-routes json",
            ]
        )
        return route_prefixes(value), as_loop_filtered_prefixes(value), True
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
        return set(), set(), False


def zebra_available() -> bool:
    """Prove the independent zebra daemon is serving its VTY socket."""
    try:
        command_json(["vtysh", "-c", "show ip route json"])
        return True
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
        return False


def kernel_bgp_prefixes() -> tuple[set[str], bool]:
    """Read BGP-owned IPv4 routes from the kernel, not bgpd's RIB."""
    try:
        routes = command_json(["ip", "-j", "-4", "route", "show", "table", "main"])
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
        return set(), False
    if not isinstance(routes, list):
        return set(), False
    found: set[str] = set()
    for route in routes:
        if not isinstance(route, dict):
            continue
        protocol = str(route.get("protocol", "")).lower()
        if protocol not in {"bgp", "186"}:
            continue
        destination = route.get("dst")
        if not isinstance(destination, str):
            continue
        try:
            network = ipaddress.ip_network(destination, strict=False)
        except ValueError:
            continue
        if network.version == 4 and network.prefixlen > 0:
            found.add(str(network))
    return found, True


def metric(name: str, value: int | float, labels: dict[str, str]) -> str:
    encoded = ",".join(
        f'{key}="{str(labels[key]).replace(chr(92), chr(92) * 2).replace(chr(34), chr(92) + chr(34)).replace(chr(10), chr(92) + "n")}"'
        for key in sorted(labels)
    )
    return f"{name}{{{encoded}}} {value}\n"


def nft_counters() -> tuple[int, int, int, bool]:
    """Read the capability-isolated collector's atomic nftables snapshot."""
    try:
        if time.time() - NFT_COUNTERS_FILE.stat().st_mtime > NFT_COUNTERS_MAX_AGE_SECONDS:
            return 0, 0, 0, False
        ruleset = json.loads(NFT_COUNTERS_FILE.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return 0, 0, 0, False
    packets = bytes_count = dropped = 0
    for item in ruleset.get("nftables", []):
        rule = item.get("rule", {}) if isinstance(item, dict) else {}
        comment = str(rule.get("comment", ""))
        for expression in rule.get("expr", []):
            counter = expression.get("counter") if isinstance(expression, dict) else None
            if isinstance(counter, dict):
                rule_packets = int(counter.get("packets", 0))
                packets += rule_packets
                bytes_count += int(counter.get("bytes", 0))
                if "drop" in comment or "invalid" in comment or "deny" in comment:
                    dropped += rule_packets
    return packets, bytes_count, dropped, True


def collect() -> tuple[str, bool, bool]:
    healthy = True
    try:
        summary = command_json(["vtysh", "-c", "show bgp ipv4 unicast summary json"])
        peers = find_peer_map(summary)
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError):
        healthy = False
        peers = {}

    healthy = healthy and bool(EXPECTED_PEERS)
    lines = [
        "# HELP kube_dc_bgp_session_up Whether the gateway BGP session is Established.\n",
        "# TYPE kube_dc_bgp_session_up gauge\n",
        "# HELP kube_dc_bgp_session_flaps_total Observed BGP state transitions away from Established.\n",
        "# TYPE kube_dc_bgp_session_flaps_total counter\n",
    ]
    accepted_imports: set[str] = set()
    with state_lock:
        for peer in sorted(EXPECTED_PEERS):
            details = peers.get(peer, {})
            details = details if isinstance(details, dict) else {}
            peer_state = str(details.get("state", "Unknown"))
            if peer_state != "Established":
                healthy = False
            previous = last_peer_states.get(peer)
            if previous == "Established" and peer_state != "Established":
                peer_flaps[peer] = peer_flaps.get(peer, 0) + 1
            last_peer_states[peer] = peer_state
            labels = {**BASE_LABELS, "peer": peer}
            received_routes, as_loop_filtered, received_ok = (
                peer_received_route_table(peer)
            )
            accepted_routes, accepted_ok = peer_route_table(peer, "routes")
            if accepted_ok and peer_state == "Established":
                accepted_imports.update(accepted_routes)
            advertised_routes, advertised_ok = peer_route_table(peer, "advertised-routes")
            received = len(received_routes) if received_ok else int(details.get("pfxRcd", details.get("prefixReceived", 0)) or 0)
            accepted = len(accepted_routes) if accepted_ok else int(details.get("pfxRcd", details.get("prefixReceived", 0)) or 0)
            advertised = len(advertised_routes) if advertised_ok else int(details.get("pfxSnt", details.get("prefixSent", 0)) or 0)
            policy_received_routes = received_routes.difference(as_loop_filtered)
            rejected_routes = {
                prefix
                for prefix in policy_received_routes
                if not prefix_allowed(prefix)
            }
            rejected = len(rejected_routes) if received_ok else max(0, received - accepted)
            leaked_routes = advertised_routes.difference(ALLOWED_EXPORTS) if advertised_ok else set()
            max_prefix_exceeded = (
                len(policy_received_routes) > MAX_PREFIXES
                if received_ok
                else received > MAX_PREFIXES
            )
            for prefix in rejected_routes:
                seen_policy_violations.add((peer, "unexpected-import", prefix))
            for prefix in leaked_routes:
                seen_policy_violations.add((peer, "route-leak", prefix))
            if max_prefix_exceeded:
                seen_policy_violations.add((peer, "maximum-prefix", str(received)))
                healthy = False
            if leaked_routes:
                healthy = False
            lines.extend(
                [
                    metric("kube_dc_bgp_session_up", int(peer_state == "Established"), labels),
                    metric("kube_dc_bgp_session_flaps_total", peer_flaps.get(peer, 0), labels),
                    metric("kube_dc_bgp_routes_received", received, labels),
                    metric("kube_dc_bgp_routes_accepted", accepted, labels),
                    metric("kube_dc_bgp_routes_rejected", rejected, labels),
                    metric(
                        "kube_dc_bgp_as_loop_filtered_routes",
                        len(as_loop_filtered),
                        labels,
                    ),
                    metric("kube_dc_bgp_routes_advertised", advertised, labels),
                    metric("kube_dc_bgp_max_prefix_exceeded", int(max_prefix_exceeded), labels),
                    metric("kube_dc_bgp_unexpected_routes", rejected, labels),
                    metric("kube_dc_route_leak_detected", len(leaked_routes), labels),
                ]
            )

    missing_imports = 0
    for allowed in ALLOWED_IMPORTS:
        if IMPORT_MATCH_MODE == "exact":
            covered = str(allowed) in accepted_imports
        else:
            covered = any(
                ipaddress.ip_network(route, strict=False).subnet_of(allowed)
                and prefix_allowed(route)
                for route in accepted_imports
            )
        if not covered:
            missing_imports += 1
    if missing_imports:
        healthy = False

    # bgpd's accepted-route table is not sufficient proof of forwarding. Zebra
    # is an independent daemon and can die while every peer remains Established.
    # Require its VTY endpoint and the actual kernel BGP routes before keeping
    # this replica Ready; liveness restarts the pod when zebra itself is gone.
    zebra_up = zebra_available()
    fib_imports, fib_read_ok = kernel_bgp_prefixes()
    missing_fib_routes = len(accepted_imports.difference(fib_imports))
    fib_synced = zebra_up and fib_read_ok and missing_fib_routes == 0
    if not fib_synced:
        healthy = False

    packets, bytes_count, dropped, nft_counters_available = nft_counters()
    lines.extend(
        [
            metric("kube_dc_routed_network_packets", packets, BASE_LABELS),
            metric("kube_dc_routed_network_bytes", bytes_count, BASE_LABELS),
            metric("kube_dc_routed_network_dropped_packets", dropped, BASE_LABELS),
            metric("kube_dc_nft_counter_collector_up", int(nft_counters_available), BASE_LABELS),
            metric("kube_dc_routing_gateway_ready", int(healthy), BASE_LABELS),
            metric("kube_dc_routing_gateway_replicas", 1, BASE_LABELS),
            metric("kube_dc_route_policy_violations_total", len(seen_policy_violations), BASE_LABELS),
            metric("kube_dc_bgp_missing_imports", missing_imports, BASE_LABELS),
            metric("kube_dc_bgp_fib_synced", int(fib_synced), BASE_LABELS),
            metric("kube_dc_bgp_missing_fib_routes", missing_fib_routes, BASE_LABELS),
        ]
    )
    return "".join(lines), healthy, zebra_up


def current_snapshot() -> tuple[str, bool, bool]:
    with snapshot_lock:
        return last_snapshot, last_healthy, last_live


def collector_loop() -> None:
    global last_snapshot, last_healthy, last_live
    while True:
        try:
            output, healthy, live = collect()
        except Exception:  # Keep the HTTP health endpoint independent of FRR parsing.
            output, healthy, live = "", False, False
        with snapshot_lock:
            last_snapshot = output
            last_healthy = healthy
            last_live = live
        time.sleep(COLLECT_INTERVAL_SECONDS)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:  # noqa: N802 -- BaseHTTPRequestHandler API
        if self.path == "/healthz":
            _, _, live = current_snapshot()
            self.send_response(200 if live else 503)
            body = b"ok\n" if live else b"zebra unavailable\n"
        elif self.path == "/readyz":
            _, healthy, _ = current_snapshot()
            self.send_response(200 if healthy else 503)
            body = b"ready\n" if healthy else b"not ready\n"
        elif self.path == "/metrics":
            output, _, _ = current_snapshot()
            self.send_response(200)
            self.send_header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
            body = output.encode("utf-8")
        else:
            self.send_response(404)
            body = b"not found\n"
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:
        return


def main() -> None:
    threading.Thread(target=collector_loop, name="frr-collector", daemon=True).start()
    server = ThreadingHTTPServer((LISTEN_ADDRESS, LISTEN_PORT), Handler)
    server.serve_forever()


if __name__ == "__main__":
    main()
