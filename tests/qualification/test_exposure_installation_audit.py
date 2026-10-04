"""Inventory must expose uncertain attachment and serving identity evidence."""

import copy
import datetime
import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location(
    "audit", Path(__file__).resolve().parents[2] / "hack/audit-exposure-installation.py")
AUDIT = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(AUDIT)


def obj(kind, name, namespace="platform", group="gateway.networking.k8s.io"):
    return {"apiVersion": group + "/v1", "kind": kind,
            "metadata": {"name": name, "namespace": namespace, "uid": namespace + "-" + name,
                         "resourceVersion": "17"}, "spec": {}}


def parent(name="eg", namespace="envoy-gateway-system", kind="Gateway",
           group="gateway.networking.k8s.io"):
    return {"name": name, "namespace": namespace, "kind": kind, "group": group}


def owner(object):
    return {"apiVersion": object["apiVersion"], "kind": object["kind"],
            "name": object["metadata"]["name"], "uid": object["metadata"]["uid"], "controller": True}


class Attachments(unittest.TestCase):
    def resolve(self, route, *listeners):
        return AUDIT.gateway_attachment(route, {AUDIT.attachment_key(x): x for x in listeners},
                                        gateway=obj("Gateway", "eg", "envoy-gateway-system"))

    def test_transitive_paths_preserve_uids_and_default_namespace_at_each_hop(self):
        route = obj("TLSRoute", "tenant-route", "tenant")
        first = obj("ListenerSet", "first", "platform")
        second = obj("XListenerSet", "second", "platform", "gateway.networking.x-k8s.io")
        route["spec"]["parentRefs"] = [parent("first", "platform", "ListenerSet")]
        first["spec"]["parentRef"] = {"name": "second", "kind": "XListenerSet", "group": "gateway.networking.x-k8s.io"}
        second["spec"]["parentRef"] = parent()
        attached, findings, paths = self.resolve(route, first, second)
        self.assertTrue(attached)
        self.assertEqual(findings, [])
        self.assertEqual([x["uid"] for x in paths[0]],
                         ["tenant-tenant-route", "platform-first", "platform-second", "envoy-gateway-system-eg"])

    def test_broken_sibling_is_reported_even_with_valid_gateway_parent(self):
        route = obj("HTTPRoute", "mixed")
        route["spec"]["parentRefs"] = [parent(), parent("missing", "other", "ListenerSet")]
        attached, findings, paths = self.resolve(route)
        self.assertTrue(attached)
        self.assertEqual(len(paths), 1)
        self.assertEqual(findings[0]["finding"], "unresolved listener parent")

    def test_empty_and_unsupported_groups_never_default_to_platform_gateway(self):
        for group in ("", "foreign.example"):
            with self.subTest(group=group):
                route = obj("HTTPRoute", "bad")
                route["spec"]["parentRefs"] = [parent(group=group)]
                attached, findings, paths = self.resolve(route)
                self.assertFalse(attached)
                self.assertEqual(paths, [])
                self.assertEqual(len(findings), 1)

    def test_listener_group_is_part_of_identity(self):
        route = obj("HTTPRoute", "bad")
        route["spec"]["parentRefs"] = [parent("listener", "platform", "ListenerSet", "foreign.example")]
        listener = obj("ListenerSet", "listener")
        listener["spec"]["parentRef"] = parent()
        attached, findings, _ = self.resolve(route, listener)
        self.assertFalse(attached)
        self.assertEqual(findings[0]["finding"], "unsupported listener parent group")

    def test_foreign_gateway_chain_is_not_a_shared_attachment(self):
        route = obj("HTTPRoute", "tenant", "tenant")
        listener = obj("ListenerSet", "listener", "tenant")
        route["spec"]["parentRefs"] = [{"kind": "ListenerSet", "name": "listener"}]
        listener["spec"]["parentRef"] = {"name": "own-gateway"}
        self.assertEqual(self.resolve(route, listener), (False, [], []))

    def test_cycles_and_depth_limit_remain_visible(self):
        first = obj("ListenerSet", "first")
        second = obj("ListenerSet", "second")
        first["spec"]["parentRef"] = parent("second", "platform", "ListenerSet")
        second["spec"]["parentRef"] = parent("first", "platform", "ListenerSet")
        attached, findings, paths = self.resolve(first, first, second)
        self.assertFalse(attached)
        self.assertEqual(paths, [])
        self.assertEqual(findings[0]["finding"], "cyclic listener parent references")
        _, limited, _ = AUDIT.gateway_attachment(first, {}, visiting=frozenset(range(128)))
        self.assertIn("depth limit", limited[0]["finding"])


class ServingInventory(unittest.TestCase):
    def setUp(self):
        self.deployment = obj("Deployment", "manager", "kube-dc", "apps")
        self.rs = obj("ReplicaSet", "manager-rs", "kube-dc", "apps")
        self.rs["metadata"]["ownerReferences"] = [owner(self.deployment)]
        self.pod = obj("Pod", "manager-pod", "kube-dc", "")
        self.pod["apiVersion"] = "v1"
        self.pod["metadata"]["ownerReferences"] = [owner(self.rs)]
        self.pod["spec"]["containers"] = [{"name": "manager", "env": []}]
        self.pod["status"] = {"podIPs": [{"ip": "10.0.0.2"}], "conditions": [{"type": "Ready", "status": "True"}]}
        self.service = obj("Service", "arbitrary-webhook", "kube-dc", "")
        self.service["apiVersion"] = "v1"
        self.service["spec"]["ports"] = [{"name": "https", "port": 443, "targetPort": "webhook-server"}]
        self.configuration = obj("ValidatingWebhookConfiguration", "guard", group="admissionregistration.k8s.io")
        self.configuration["webhooks"] = [{"name": "vexposurerouteclaims.kube-dc.com", "clientConfig": {
            "service": {"namespace": "kube-dc", "name": "arbitrary-webhook", "path": "/handler"}}}]
        self.slice = obj("EndpointSlice", "webhook-slice", "kube-dc", "discovery.k8s.io")
        self.slice["metadata"]["ownerReferences"] = [owner(self.service)]
        self.slice["metadata"]["labels"] = {"kubernetes.io/service-name": "arbitrary-webhook"}
        self.slice["ports"] = [{"name": "https", "port": 9443}]
        self.slice["endpoints"] = [{"targetRef": {**owner(self.pod), "namespace": "kube-dc"},
                                    "addresses": ["10.0.0.2"], "conditions": {"ready": True, "serving": True, "terminating": False}}]

    def managers(self, pod=None, rs=None):
        return AUDIT.deployment_managers(self.deployment, [rs or self.rs], [pod or self.pod])

    def endpoints(self, slices=None, services=None):
        return AUDIT.webhook_endpoints([self.configuration], services if services is not None else {
            ("kube-dc", "arbitrary-webhook"): self.service}, slices or [self.slice], self.managers())

    def test_exact_service_and_pod_uid_chains_and_ports_are_recorded(self):
        endpoint = self.endpoints()[0]["endpoints"][0]
        self.assertTrue(endpoint["currentServiceOwner"])
        self.assertTrue(endpoint["deploymentOwnedManager"])
        self.assertTrue(endpoint["addressesMatchManagerPodIPs"])
        self.assertEqual(endpoint["slicePorts"][0]["port"], 9443)
        self.assertEqual(endpoint["servicePorts"][0]["targetPort"], "webhook-server")
        self.assertEqual(endpoint["manager"]["deployment"]["uid"], self.deployment["metadata"]["uid"])

    def test_foreign_namespace_and_forged_owner_kind_name_or_uid_are_excluded(self):
        for field, value in (("apiVersion", "foreign/v1"), ("kind", "Deployment"), ("name", "other"), ("uid", "replaced")):
            with self.subTest(field=field):
                pod = copy.deepcopy(self.pod)
                pod["metadata"]["ownerReferences"][0][field] = value
                self.assertEqual(self.managers(pod=pod), [])
        pod = copy.deepcopy(self.pod)
        pod["metadata"]["namespace"] = "tenant"
        self.assertEqual(self.managers(pod=pod), [])
        rs = copy.deepcopy(self.rs)
        rs["metadata"]["ownerReferences"][0]["uid"] = "replaced-deployment"
        self.assertEqual(self.managers(rs=rs), [])

    def test_uid_replacement_and_missing_target_remain_visible(self):
        for field, value in (("uid", "old-pod"), ("kind", "Service"), ("namespace", "tenant"), ("name", "foreign")):
            with self.subTest(field=field):
                endpoint_slice = copy.deepcopy(self.slice)
                endpoint_slice["endpoints"][0]["targetRef"][field] = value
                endpoint = self.endpoints([endpoint_slice])[0]["endpoints"][0]
                self.assertFalse(endpoint["deploymentOwnedManager"])
                self.assertIsNone(endpoint["manager"])
                self.assertEqual(endpoint["conditions"], self.slice["endpoints"][0]["conditions"])
        endpoint_slice = copy.deepcopy(self.slice)
        del endpoint_slice["endpoints"][0]["targetRef"]
        self.assertFalse(self.endpoints([endpoint_slice])[0]["endpoints"][0]["deploymentOwnedManager"])

    def test_service_owner_replacement_and_address_mismatch_are_not_hidden(self):
        endpoint_slice = copy.deepcopy(self.slice)
        endpoint_slice["metadata"]["ownerReferences"][0]["uid"] = "old-service"
        endpoint_slice["endpoints"][0]["addresses"] = ["10.0.0.99"]
        endpoint = self.endpoints([endpoint_slice])[0]["endpoints"][0]
        self.assertFalse(endpoint["currentServiceOwner"])
        self.assertEqual(endpoint["observedServiceOwnerReference"]["uid"], "old-service")
        self.assertFalse(endpoint["addressesMatchManagerPodIPs"])

    def test_missing_service_or_url_configuration_is_a_finding(self):
        self.assertEqual(self.endpoints(services={})[0]["finding"], "unresolved webhook Service")
        self.configuration["webhooks"][0]["clientConfig"] = {"url": "https://example.invalid"}
        observation = self.endpoints()[0]
        self.assertTrue(observation["URLConfigured"])
        self.assertEqual(observation["endpoints"], [])

    def test_lease_candidate_does_not_hide_expiry_or_unresolved_holder(self):
        lease = obj("Lease", "manager-lease", "kube-dc", "coordination.k8s.io")
        lease["spec"] = {"holderIdentity": "manager-pod_random-suffix", "renewTime": "2026-10-04T12:00:00Z", "leaseDurationSeconds": 15}
        now = datetime.datetime(2026, 10, 4, 12, 0, 20, tzinfo=datetime.timezone.utc)
        observation = AUDIT.leader_observation(lease, self.managers(), now)
        self.assertFalse(observation["unexpiredAtRead"])
        self.assertEqual(observation["holderPodCandidate"]["uid"], self.pod["metadata"]["uid"])
        self.assertEqual(observation["lease"]["resourceVersion"], "17")
        lease["spec"]["holderIdentity"] = "foreign-pod_random-suffix"
        self.assertIsNone(AUDIT.leader_observation(lease, self.managers(), now)["holderPodCandidate"])


if __name__ == "__main__":
    unittest.main()
