import importlib.util
import pathlib
import tempfile
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("exporter.py")
SPEC = importlib.util.spec_from_file_location("routing_gateway_exporter", MODULE_PATH)
exporter = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(exporter)


class ExporterPolicyTests(unittest.TestCase):
    def test_nft_counters_read_fresh_capability_isolated_snapshot(self):
        saved = (exporter.NFT_COUNTERS_FILE, exporter.NFT_COUNTERS_MAX_AGE_SECONDS)
        try:
            with tempfile.TemporaryDirectory() as directory:
                snapshot = pathlib.Path(directory) / "counters.json"
                snapshot.write_text(
                    '{"nftables":[{"rule":{"comment":"project to approved import",'
                    '"expr":[{"counter":{"packets":5,"bytes":320}}]}},'
                    '{"rule":{"comment":"deny transit or unapproved prefix",'
                    '"expr":[{"counter":{"packets":2,"bytes":128}}]}}]}',
                    encoding="utf-8",
                )
                exporter.NFT_COUNTERS_FILE = snapshot
                exporter.NFT_COUNTERS_MAX_AGE_SECONDS = 15
                self.assertEqual(exporter.nft_counters(), (7, 448, 2, True))
        finally:
            exporter.NFT_COUNTERS_FILE, exporter.NFT_COUNTERS_MAX_AGE_SECONDS = saved

    def test_nft_counters_report_collector_unavailable(self):
        saved = exporter.NFT_COUNTERS_FILE
        try:
            exporter.NFT_COUNTERS_FILE = pathlib.Path("/definitely/missing/nft-counters.json")
            self.assertEqual(exporter.nft_counters(), (0, 0, 0, False))
        finally:
            exporter.NFT_COUNTERS_FILE = saved

    def test_route_prefixes_understands_frr_table_shapes(self):
        value = {
            "receivedRoutes": {
                "198.51.100.0/24": [{"valid": True}],
                "203.0.113.9/24": [{"prefix": "203.0.113.0/24"}],
            },
            "peer": "192.0.2.1",
        }
        self.assertEqual(exporter.route_prefixes(value), {"198.51.100.0/24", "203.0.113.0/24"})

    def test_as_loop_filter_identifies_only_routes_with_local_as_in_every_path(self):
        value = {
            "localAS": 65002,
            "receivedRoutes": {
                "10.0.30.0/24": {"path": "65001 65002"},
                "198.51.100.0/24": {"path": "65001"},
                "203.0.113.0/24": [
                    {"path": "65001 65002"},
                    {"path": "65001"},
                ],
            },
        }
        self.assertEqual(
            exporter.as_loop_filtered_prefixes(value), {"10.0.30.0/24"}
        )

    def test_own_as_reflection_does_not_trip_import_limits(self):
        saved = (
            exporter.EXPECTED_PEERS,
            exporter.ALLOWED_IMPORTS,
            exporter.ALLOWED_EXPORTS,
            exporter.MAX_PREFIXES,
            exporter.command_json,
        )
        try:
            exporter.EXPECTED_PEERS = {"192.0.2.1"}
            exporter.ALLOWED_IMPORTS = {
                exporter.ipaddress.ip_network("198.51.100.0/24")
            }
            exporter.ALLOWED_EXPORTS = {"10.0.30.0/24"}
            exporter.MAX_PREFIXES = 1

            def command(argv):
                command_text = argv[-1]
                if argv[0] == "ip":
                    return [{"dst": "198.51.100.0/24", "protocol": "bgp"}]
                if command_text == "show bgp ipv4 unicast summary json":
                    return {"peers": {"192.0.2.1": {"state": "Established"}}}
                if "received-routes" in command_text:
                    return {
                        "localAS": 65002,
                        "receivedRoutes": {
                            "10.0.30.0/24": {"path": "65001 65002"},
                            "198.51.100.0/24": {"path": "65001"},
                        },
                    }
                if "advertised-routes" in command_text:
                    return {"routes": {"10.0.30.0/24": {}}}
                if command_text.endswith(" routes json"):
                    return {"routes": {"198.51.100.0/24": {}}}
                return {"nftables": []}

            exporter.command_json = command
            body, healthy, live = exporter.collect()
            self.assertTrue(live)
            self.assertTrue(healthy)
            self.assertIn("kube_dc_bgp_as_loop_filtered_routes", body)
            self.assertIn("kube_dc_bgp_max_prefix_exceeded", body)
            self.assertIn("kube_dc_bgp_routes_rejected", body)
        finally:
            (
                exporter.EXPECTED_PEERS,
                exporter.ALLOWED_IMPORTS,
                exporter.ALLOWED_EXPORTS,
                exporter.MAX_PREFIXES,
                exporter.command_json,
            ) = saved

    def test_default_route_is_never_allowed(self):
        saved = exporter.ALLOWED_IMPORTS
        try:
            exporter.ALLOWED_IMPORTS = {exporter.ipaddress.ip_network("0.0.0.0/0")}
            self.assertFalse(exporter.prefix_allowed("0.0.0.0/0"))
        finally:
            exporter.ALLOWED_IMPORTS = saved

    def test_more_specific_policy_is_bounded(self):
        saved = (exporter.ALLOWED_IMPORTS, exporter.IMPORT_MATCH_MODE, exporter.IMPORT_MIN_PREFIX_LENGTH, exporter.IMPORT_MAX_PREFIX_LENGTH)
        try:
            exporter.ALLOWED_IMPORTS = {exporter.ipaddress.ip_network("198.51.100.0/24")}
            exporter.IMPORT_MATCH_MODE = "exact-or-more-specific"
            exporter.IMPORT_MIN_PREFIX_LENGTH = 24
            exporter.IMPORT_MAX_PREFIX_LENGTH = 28
            self.assertTrue(exporter.prefix_allowed("198.51.100.0/25"))
            self.assertFalse(exporter.prefix_allowed("198.51.100.0/29"))
            self.assertFalse(exporter.prefix_allowed("203.0.113.0/24"))
        finally:
            (exporter.ALLOWED_IMPORTS, exporter.IMPORT_MATCH_MODE, exporter.IMPORT_MIN_PREFIX_LENGTH, exporter.IMPORT_MAX_PREFIX_LENGTH) = saved

    def test_policy_violation_makes_max_prefix_and_export_leaks_unready(self):
        saved = (
            exporter.EXPECTED_PEERS,
            exporter.ALLOWED_IMPORTS,
            exporter.ALLOWED_EXPORTS,
            exporter.MAX_PREFIXES,
            exporter.command_json,
        )
        try:
            exporter.EXPECTED_PEERS = {"192.0.2.1"}
            exporter.ALLOWED_IMPORTS = {exporter.ipaddress.ip_network("198.51.100.0/24")}
            exporter.ALLOWED_EXPORTS = {"10.0.0.0/24"}
            exporter.MAX_PREFIXES = 1

            def command(argv):
                command_text = argv[-1]
                if command_text == "show bgp ipv4 unicast summary json":
                    return {"peers": {"192.0.2.1": {"state": "Established"}}}
                if "received-routes" in command_text:
                    return {"routes": {"198.51.100.0/24": {}, "203.0.113.0/24": {}}}
                if "advertised-routes" in command_text:
                    return {"routes": {"10.0.0.0/24": {}, "10.1.0.0/24": {}}}
                if command_text.endswith(" routes json"):
                    return {"routes": {"198.51.100.0/24": {}}}
                return {"nftables": []}

            exporter.command_json = command
            body, healthy, _ = exporter.collect()
            self.assertFalse(healthy)
            self.assertIn("kube_dc_bgp_max_prefix_exceeded", body)
            self.assertIn("kube_dc_route_leak_detected", body)
        finally:
            (
                exporter.EXPECTED_PEERS,
                exporter.ALLOWED_IMPORTS,
                exporter.ALLOWED_EXPORTS,
                exporter.MAX_PREFIXES,
                exporter.command_json,
            ) = saved

    def test_missing_approved_import_makes_established_session_unready(self):
        saved = (
            exporter.EXPECTED_PEERS,
            exporter.ALLOWED_IMPORTS,
            exporter.ALLOWED_EXPORTS,
            exporter.command_json,
        )
        try:
            exporter.EXPECTED_PEERS = {"192.0.2.1"}
            exporter.ALLOWED_IMPORTS = {exporter.ipaddress.ip_network("198.51.100.0/24")}
            exporter.ALLOWED_EXPORTS = {"10.0.0.0/24"}

            def command(argv):
                command_text = argv[-1]
                if command_text == "show bgp ipv4 unicast summary json":
                    return {"peers": {"192.0.2.1": {"state": "Established"}}}
                if "advertised-routes" in command_text:
                    return {"routes": {"10.0.0.0/24": {}}}
                return {"routes": {}}

            exporter.command_json = command
            body, healthy, _ = exporter.collect()
            self.assertFalse(healthy)
            self.assertIn("kube_dc_bgp_missing_imports", body)
            self.assertIn(" 1\n", body)
        finally:
            (
                exporter.EXPECTED_PEERS,
                exporter.ALLOWED_IMPORTS,
                exporter.ALLOWED_EXPORTS,
                exporter.command_json,
            ) = saved

    def test_bgp_rib_without_kernel_fib_is_unready(self):
        saved = (
            exporter.EXPECTED_PEERS,
            exporter.ALLOWED_IMPORTS,
            exporter.ALLOWED_EXPORTS,
            exporter.command_json,
        )
        try:
            exporter.EXPECTED_PEERS = {"192.0.2.1"}
            exporter.ALLOWED_IMPORTS = {exporter.ipaddress.ip_network("198.51.100.0/24")}
            exporter.ALLOWED_EXPORTS = {"10.0.0.0/24"}

            def command(argv):
                command_text = argv[-1]
                if argv[0] == "ip":
                    return []
                if command_text == "show bgp ipv4 unicast summary json":
                    return {"peers": {"192.0.2.1": {"state": "Established"}}}
                if command_text.endswith(" routes json"):
                    return {"routes": {"198.51.100.0/24": {}}}
                if "advertised-routes" in command_text:
                    return {"routes": {"10.0.0.0/24": {}}}
                return {"nftables": []}

            exporter.command_json = command
            body, healthy, live = exporter.collect()
            self.assertTrue(live)
            self.assertFalse(healthy)
            self.assertIn("kube_dc_bgp_fib_synced", body)
            self.assertIn("kube_dc_bgp_missing_fib_routes", body)
        finally:
            (
                exporter.EXPECTED_PEERS,
                exporter.ALLOWED_IMPORTS,
                exporter.ALLOWED_EXPORTS,
                exporter.command_json,
            ) = saved

    def test_zebra_loss_fails_liveness_even_when_bgp_is_established(self):
        saved = (
            exporter.EXPECTED_PEERS,
            exporter.ALLOWED_IMPORTS,
            exporter.ALLOWED_EXPORTS,
            exporter.command_json,
        )
        try:
            exporter.EXPECTED_PEERS = {"192.0.2.1"}
            exporter.ALLOWED_IMPORTS = {exporter.ipaddress.ip_network("198.51.100.0/24")}
            exporter.ALLOWED_EXPORTS = {"10.0.0.0/24"}

            def command(argv):
                command_text = argv[-1]
                if command_text == "show ip route json":
                    raise exporter.subprocess.CalledProcessError(1, argv)
                if argv[0] == "ip":
                    return [{"dst": "198.51.100.0/24", "protocol": "bgp"}]
                if command_text == "show bgp ipv4 unicast summary json":
                    return {"peers": {"192.0.2.1": {"state": "Established"}}}
                if command_text.endswith(" routes json"):
                    return {"routes": {"198.51.100.0/24": {}}}
                if "advertised-routes" in command_text:
                    return {"routes": {"10.0.0.0/24": {}}}
                return {"nftables": []}

            exporter.command_json = command
            _, healthy, live = exporter.collect()
            self.assertFalse(live)
            self.assertFalse(healthy)
        finally:
            (
                exporter.EXPECTED_PEERS,
                exporter.ALLOWED_IMPORTS,
                exporter.ALLOWED_EXPORTS,
                exporter.command_json,
            ) = saved

    def test_established_route_is_ready_only_when_zebra_and_kernel_fib_agree(self):
        saved = (
            exporter.EXPECTED_PEERS,
            exporter.ALLOWED_IMPORTS,
            exporter.ALLOWED_EXPORTS,
            exporter.command_json,
        )
        try:
            exporter.EXPECTED_PEERS = {"192.0.2.1"}
            exporter.ALLOWED_IMPORTS = {exporter.ipaddress.ip_network("198.51.100.0/24")}
            exporter.ALLOWED_EXPORTS = {"10.0.0.0/24"}

            def command(argv):
                command_text = argv[-1]
                if argv[0] == "ip":
                    return [{"dst": "198.51.100.0/24", "protocol": "bgp"}]
                if command_text == "show bgp ipv4 unicast summary json":
                    return {"peers": {"192.0.2.1": {"state": "Established"}}}
                if "advertised-routes" in command_text:
                    return {"routes": {"10.0.0.0/24": {}}}
                if command_text.endswith(" routes json"):
                    return {"routes": {"198.51.100.0/24": {}}}
                return {"nftables": []}

            exporter.command_json = command
            body, healthy, live = exporter.collect()
            self.assertTrue(live)
            self.assertTrue(healthy)
            self.assertIn("kube_dc_bgp_fib_synced", body)
            self.assertIn("kube_dc_bgp_missing_fib_routes", body)
        finally:
            (
                exporter.EXPECTED_PEERS,
                exporter.ALLOWED_IMPORTS,
                exporter.ALLOWED_EXPORTS,
                exporter.command_json,
            ) = saved


if __name__ == "__main__":
    unittest.main()
