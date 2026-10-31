#!/usr/bin/env python3
"""Tests for per-replica FRR router-ID and MED rendering."""

import contextlib
import io
import os
import pathlib
import sys
import tempfile
import unittest
from unittest import mock

import render_config


class RenderConfigTest(unittest.TestCase):
    def render_password(self, value: str | bytes) -> tuple[int, str, str]:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            template, output, password = (root / name for name in ("template", "output", "password"))
            template.write_text("neighbor 192.0.2.1 password __KUBE_DC_BGP_PASSWORD__\n")
            password.write_bytes(value if isinstance(value, bytes) else value.encode("utf-8"))
            errors = io.StringIO()
            with (
                mock.patch.object(sys, "argv", ["render_config.py", str(template), str(output), str(password)]),
                contextlib.redirect_stderr(errors),
            ):
                result = render_config.main()
            return result, output.read_text() if output.exists() else "", errors.getvalue()

    def test_password_reaches_frr_without_json_quotes_or_escaping(self) -> None:
        for value in ("plain-secret", 'literal"quote\\backslash', "!#$%&'()*+,-./:;<=>?@[]^_`{|}~", "a" * 80):
            with self.subTest(value=value):
                result, output, errors = self.render_password(value + "\n")
                self.assertEqual(result, 0)
                self.assertEqual(output, f"neighbor 192.0.2.1 password {value}\n")
                self.assertEqual(errors, "")

    def test_invalid_password_fails_closed_without_writing_or_disclosing_it(self) -> None:
        for value in ("", "a" * 81, "secret with spaces", "secret\ttab", "secret\r", "secret\x00", "sëcret", "secret\nrouter bgp 1", "secret\n\n"):
            with self.subTest(value=value):
                result, output, errors = self.render_password(value)
                self.assertEqual(result, 1)
                self.assertEqual(output, "")
                self.assertIn("1–80 printable ASCII", errors)
                if value:
                    self.assertNotIn(value, errors)

    def test_invalid_utf8_fails_without_disclosing_secret_bytes(self) -> None:
        result, output, errors = self.render_password(b"private-value-\xff")
        self.assertEqual(result, 1)
        self.assertEqual(output, "")
        self.assertIn("invalid encoding", errors)
        self.assertNotIn("private-value", errors)

    def test_secondary_gateway_gets_higher_export_med(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            template = root / "template.conf"
            output = root / "frr.conf"
            password = root / "password"
            template.write_text(
                "bgp router-id __KUBE_DC_ROUTER_ID__\n"
                " set metric __KUBE_DC_EXPORT_MED__\n",
                encoding="utf-8",
            )
            with (
                mock.patch.object(sys, "argv", ["render_config.py", str(template), str(output), str(password)]),
                mock.patch.dict(os.environ, {"ROUTING_LINK_IPS": "100.65.0.2,100.65.0.3"}, clear=False),
                mock.patch.object(render_config, "transit_router_id", return_value="192.0.2.2"),
                mock.patch.object(render_config, "routing_link_address", return_value="100.65.0.3"),
            ):
                self.assertEqual(render_config.main(), 0)
            self.assertEqual(output.read_text(encoding="utf-8"), "bgp router-id 192.0.2.2\n set metric 100\n")

    def test_routing_link_ip_must_belong_to_ordered_allocation(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "absent from ROUTING_LINK_IPS"):
            with mock.patch.dict(os.environ, {"ROUTING_LINK_IPS": "100.65.0.2,100.65.0.3"}, clear=False):
                render_config.export_med("100.65.0.4")


if __name__ == "__main__":
    unittest.main()
