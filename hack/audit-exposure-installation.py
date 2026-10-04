#!/usr/bin/env python3
"""Read exposure ownership and serving configuration without exporting credentials."""

import argparse
import datetime
import hashlib
import json
from pathlib import Path
import subprocess


GATEWAY = ("envoy-gateway-system", "eg")
MANAGED = "service-lb-controller"
LABEL = "kube-dc.com/source-service"
GATEWAY_GROUP = "gateway.networking.k8s.io"
LISTENER_KINDS = {"ListenerSet", "XListenerSet"}


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def identity(obj):
    meta = obj["metadata"]
    return {"apiVersion": obj.get("apiVersion"), "kind": obj.get("kind"), "namespace": meta.get("namespace"),
            "name": meta["name"], "uid": meta["uid"],
            "resourceVersion": meta.get("resourceVersion")}


def controller_owner(obj, kind):
    return next((owner for owner in obj["metadata"].get("ownerReferences", [])
                 if owner.get("controller") and owner["kind"] == kind), None)


def parent_refs(route):
    parents = list(route.get("spec", {}).get("parentRefs", []))
    if route.get("kind") in LISTENER_KINDS and route.get("spec", {}).get("parentRef"):
        parents.append(route["spec"]["parentRef"])
    return parents


def attachment_key(obj):
    return (obj["apiVersion"].split("/")[0], obj["kind"],
            obj["metadata"]["namespace"], obj["metadata"]["name"])


def gateway_attachment(route, listeners, visiting=frozenset(), gateway=None):
    """Resolve spec references only; acceptance and allowedRoutes need separate checks."""
    key = attachment_key(route)
    if key in visiting:
        return False, [{**identity(route), "finding": "cyclic listener parent references"}], []
    if len(visiting) >= 128:
        return False, [{**identity(route), "finding": "listener parent path exceeds inventory depth limit"}], []
    attached, findings, paths = False, [], []
    visiting = visiting | {key}
    for parent in parent_refs(route):
        namespace = parent.get("namespace", route["metadata"]["namespace"])
        group, kind = parent.get("group", GATEWAY_GROUP), parent.get("kind", "Gateway")
        if (group, kind, namespace, parent.get("name")) == (GATEWAY_GROUP, "Gateway", *GATEWAY):
            attached = True
            paths.append([identity(route), identity(gateway) if gateway else
                          {"kind": "Gateway", "namespace": namespace, "name": parent["name"], "uid": None}])
        elif kind in LISTENER_KINDS:
            expected_group = GATEWAY_GROUP if kind == "ListenerSet" else "gateway.networking.x-k8s.io"
            if group != expected_group:
                findings.append({**identity(route), "finding": "unsupported listener parent group", "parentRef": parent})
                continue
            listener = listeners.get((group, kind, namespace, parent.get("name")))
            if listener is None:
                findings.append({**identity(route), "finding": "unresolved listener parent",
                                 "parentRef": parent})
                continue
            reaches_gateway, unresolved, parent_paths = gateway_attachment(listener, listeners, visiting, gateway)
            attached = attached or reaches_gateway
            findings.extend(unresolved)
            paths.extend([[identity(route)] + path for path in parent_paths])
        elif group != GATEWAY_GROUP or kind != "Gateway":
            findings.append({**identity(route), "finding": "unsupported parent group or kind", "parentRef": parent})
    return attached, findings, paths


def webhook_endpoints(configurations, services, slices, managers):
    """Bind client Service and EndpointSlice owner/target UIDs without claiming traffic."""
    pods = {pod["uid"]: pod for pod in managers}
    observations = []
    for configuration in configurations:
        for hook in configuration.get("webhooks", []):
            if hook["name"] != "vexposurerouteclaims.kube-dc.com":
                continue
            reference = hook.get("clientConfig", {}).get("service")
            observation = {"configuration": identity(configuration),
                           "webhook": hook["name"], "serviceReference": reference,
                           "matchingSliceCount": 0, "endpoints": [],
                           "limit": "EndpointSlice inventory; no TLS/admission request performed"}
            observations.append(observation)
            if reference is None:
                observation["finding"] = "webhook has no Service reference"
                observation["URLConfigured"] = "url" in hook.get("clientConfig", {})
                continue
            service = services.get((reference["namespace"], reference["name"]))
            if service is None:
                observation["finding"] = "unresolved webhook Service"
                continue
            observation["service"] = identity(service)
            for endpoint_slice in slices:
                meta = endpoint_slice["metadata"]
                if meta["namespace"] != reference["namespace"] or meta.get("labels", {}).get("kubernetes.io/service-name") != reference["name"]:
                    continue
                observation["matchingSliceCount"] += 1
                owner = controller_owner(endpoint_slice, "Service")
                current_service_owner = bool(owner and owner.get("apiVersion") == "v1" and owner["name"] == reference["name"] and owner["uid"] == service["metadata"]["uid"])
                for endpoint in endpoint_slice.get("endpoints", []):
                    target = endpoint.get("targetRef", {})
                    pod = pods.get(target.get("uid"))
                    matches = bool(pod and target.get("apiVersion", "v1") == "v1" and target.get("kind") == "Pod" and target.get("namespace", meta["namespace"]) == pod["namespace"] and target.get("name") == pod["name"])
                    addresses = endpoint.get("addresses", [])
                    addresses_match = bool(matches and addresses and set(addresses) <= set(pod["podIPs"]))
                    observation["endpoints"].append({"slice": identity(endpoint_slice),
                        "observedServiceOwnerReference": owner,
                        "currentServiceOwner": current_service_owner,
                        "sliceDeleting": bool(meta.get("deletionTimestamp")),
                        "addresses": addresses, "addressesMatchManagerPodIPs": addresses_match,
                        "slicePorts": endpoint_slice.get("ports", []),
                        "servicePorts": service.get("spec", {}).get("ports", []),
                        "targetRef": target, "conditions": endpoint.get("conditions", {}),
                        "deploymentOwnedManager": matches,
                        "manager": pod if matches else None})
    return observations


def deployment_managers(manager, replica_set_objects, pod_objects):
    replica_sets = {}
    for rs in replica_set_objects:
        owner = controller_owner(rs, "Deployment")
        if (rs["metadata"]["namespace"] == manager["metadata"]["namespace"] and owner and
                owner.get("apiVersion") == "apps/v1" and owner["name"] == manager["metadata"]["name"] and owner["uid"] == manager["metadata"]["uid"]):
            replica_sets[rs["metadata"]["uid"]] = rs
    managers = []
    for pod in pod_objects:
        owner = controller_owner(pod, "ReplicaSet")
        rs = replica_sets.get(owner["uid"]) if owner else None
        if not (rs and owner.get("apiVersion") == "apps/v1" and owner["name"] == rs["metadata"]["name"] and pod["metadata"]["namespace"] == rs["metadata"]["namespace"]):
            continue
        managers.append({**identity(pod), "ready": any(c["type"] == "Ready" and c["status"] == "True" for c in pod.get("status", {}).get("conditions", [])),
                         "replicaSet": identity(rs), "deployment": identity(manager),
                         "podIPs": [entry["ip"] for entry in pod.get("status", {}).get("podIPs", [])] or ([pod["status"]["podIP"]] if pod.get("status", {}).get("podIP") else []),
                         "deleting": bool(pod["metadata"].get("deletionTimestamp")),
                         "imageIDs": [c.get("imageID") for c in pod.get("status", {}).get("containerStatuses", [])],
                         "exposureEnv": {e["name"]: e.get("value") for container in pod["spec"]["containers"] for e in container.get("env", []) if e["name"].startswith(("EXPOSURE_", "FIP_SOURCE_POLICY_"))}})
    return managers


def leader_observation(lease, managers, now):
    spec = lease.get("spec", {})
    holder = spec.get("holderIdentity", "")
    pods = [pod for pod in managers if holder == pod["name"] or holder.startswith(pod["name"] + "_")]
    renewed, unexpired = spec.get("renewTime"), None
    if renewed and spec.get("leaseDurationSeconds") is not None:
        try:
            expires = datetime.datetime.fromisoformat(renewed.replace("Z", "+00:00")) + datetime.timedelta(seconds=spec["leaseDurationSeconds"])
            unexpired = expires > now
        except (TypeError, ValueError):
            pass
    return {"lease": identity(lease), "holderIdentity": holder, "renewTime": renewed,
            "leaseDurationSeconds": spec.get("leaseDurationSeconds"), "unexpiredAtRead": unexpired,
            "holderPodCandidate": pods[0] if len(pods) == 1 else None,
            "limit": "Holder name/suffix is not a Pod UID; candidate resolution only. Does not identify the actual leader or writer of prior policy changes"}


def audit(kubeconfig):
    started = datetime.datetime.now(datetime.timezone.utc)
    helper_hash = hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
    kubectl = ["kubectl", "--kubeconfig", str(kubeconfig), "--request-timeout=30s"]

    def get(resource, namespace=None, name=None):
        args = kubectl + ["get", resource]
        args += ["-n", namespace] if namespace else ["-A"]
        if name:
            args.append(name)
        return json.loads(subprocess.check_output(args + ["-o", "json"], text=True))

    def items(resource):
        return get(resource)["items"]

    namespaces = {obj["metadata"]["name"]: obj for obj in items("namespaces")}
    projects = items("projects.kube-dc.com")
    project_namespaces = {obj.get("status", {}).get("namespace") for obj in projects}
    services = {(obj["metadata"]["namespace"], obj["metadata"]["name"]): obj for obj in items("services")}
    gateway = get("gateways.gateway.networking.k8s.io", *GATEWAY)
    routes = []
    attachment_resources = [resource + ".gateway.networking.k8s.io" for resource in ("httproutes", "tlsroutes", "grpcroutes", "tcproutes", "udproutes", "listenersets")]
    attachment_resources.append("xlistenersets.gateway.networking.x-k8s.io")
    installed = set(subprocess.check_output(kubectl + ["api-resources", "-o", "name"], text=True).splitlines())
    attachments = [obj for resource in attachment_resources if resource in installed for obj in items(resource)]
    listeners = {attachment_key(obj): obj for obj in attachments if obj["kind"] in LISTENER_KINDS}
    attachment_findings = []
    for route in attachments:
        attached, findings, paths = gateway_attachment(route, listeners, gateway=gateway)
        attachment_findings.extend(findings)
        if not attached:
            continue
        meta = route["metadata"]
        owner = controller_owner(route, "Service")
        service = services.get((meta["namespace"], owner["name"])) if owner else None
        owned = bool(owner and owner["apiVersion"] == "v1" and service and owner["uid"] == service["metadata"]["uid"])
        rules = route["spec"].get("rules", [])
        routes.append({**identity(route), "specSHA256": digest(route["spec"]),
                       "configuredGatewayPaths": paths,
                       "class": "Service-owned" if owned else "tenant-native" if meta["namespace"] in project_namespaces else "non-Project-native",
                       "currentServiceOwner": owned,
                       "forwardBackendRefCount": sum(len(rule.get("backendRefs", [])) for rule in rules),
                       "fluxSource": {key: value for key, value in meta.get("labels", {}).items() if key.startswith(("kustomize.toolkit.fluxcd.io/", "helm.toolkit.fluxcd.io/"))}})
    certificates = []
    certificate_findings = []
    acme_namespaces = set()
    issuers = {(obj["metadata"]["namespace"], obj["metadata"]["name"]): obj for obj in items("issuers.cert-manager.io")}
    cluster_issuers = {obj["metadata"]["name"]: obj for obj in items("clusterissuers.cert-manager.io")}
    for cert in items("certificates.cert-manager.io"):
        meta = cert["metadata"]
        owner = controller_owner(cert, "Service")
        service = services.get((meta["namespace"], owner["name"])) if owner else None
        issuer_ref = cert["spec"].get("issuerRef", {})
        issuer_kind = issuer_ref.get("kind", "Issuer")
        issuer = issuers.get((meta["namespace"], issuer_ref.get("name"))) if issuer_kind == "Issuer" else cluster_issuers.get(issuer_ref.get("name")) if issuer_kind == "ClusterIssuer" else None
        if issuer is None:
            certificate_findings.append({**identity(cert), "finding": "unresolved issuer", "issuerRef": issuer_ref})
            continue
        http01 = any(solver.get("http01", {}).get("gatewayHTTPRoute") for solver in issuer.get("spec", {}).get("acme", {}).get("solvers", []))
        if not http01:
            continue
        owned = bool(owner and owner["apiVersion"] == "v1" and service and owner["uid"] == service["metadata"]["uid"] and not service["metadata"].get("deletionTimestamp"))
        labels_valid = bool(owned and meta.get("labels", {}).get("kube-dc.com/managed-by") == MANAGED and meta.get("labels", {}).get(LABEL) == service["metadata"]["name"])
        if not owner and meta["namespace"] not in project_namespaces:
            acme_namespaces.add(meta["namespace"])
        certificates.append({**identity(cert), "currentServiceOwner": owned, "ownershipLabelsValid": labels_valid,
                             "projectNamespace": meta["namespace"] in project_namespaces,
                             "dnsNames": cert["spec"].get("dnsNames", []),
                             "ready": any(c["type"] == "Ready" and c["status"] == "True" and c.get("observedGeneration") == meta.get("generation") for c in cert.get("status", {}).get("conditions", []))})
    manager = get("deployments.apps", "kube-dc", "kube-dc-manager")
    managers = deployment_managers(manager, items("replicasets.apps"), items("pods"))
    helm = get("helmreleases.helm.toolkit.fluxcd.io", "kube-dc", "kube-dc")
    values = helm["spec"].get("values", {})
    available = values.get("backend", {}).get("exposureSourcePolicy", {})
    native_namespaces = {route["namespace"] for route in routes if route["class"] == "non-Project-native"}
    subnets = [{**identity(subnet), "cidr": subnet["spec"].get("cidrBlock"), "availableIPv4": subnet.get("status", {}).get("v4availableIPs")} for subnet in items("subnets.kubeovn.io") if subnet["metadata"]["name"] in ("ext-public", "ext-cloud")]
    webhooks = items("validatingwebhookconfigurations")
    endpoint_inventory = webhook_endpoints(webhooks, services, items("endpointslices.discovery.k8s.io"), managers)
    leases = [lease for lease in items("leases.coordination.k8s.io") if lease["metadata"]["namespace"] == "kube-dc" and lease["metadata"]["name"] == "b02d5b66.kube-dc.com"]
    now = datetime.datetime.now(datetime.timezone.utc)
    return {"auditStartedAtUTC": started.isoformat(), "auditedAtUTC": now.isoformat(), "readOnly": True,
            "auditHelperSHA256": helper_hash,
            "auditHelperFileUnchangedDuringRead": helper_hash == hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            "snapshotLimit": "Sequential GETs are not an atomic snapshot; object resource versions and start/end times are recorded. Concurrent updates require re-reading and actual endpoint/TLS/admission checks.",
            "productionQualified": False, "evidenceLimit": "GET inventory; no authenticated admission, traffic, renewal, or lifecycle qualification",
            "helmGeneration": helm["metadata"]["generation"], "chart": helm["spec"]["chartRef"]["name"] if "chartRef" in helm["spec"] else helm["spec"]["chart"]["spec"]["version"],
            "consoleCapabilities": available, "guardConfigured": values.get("manager", {}).get("webhook", {}).get("protectExposureRoutes", False),
            "guardInstalled": any(w["name"] == "vexposurerouteclaims.kube-dc.com" for config in webhooks for w in config.get("webhooks", [])),
            "gateway": {**identity(gateway), "specSHA256": digest(gateway["spec"]), "listenerCount": len(gateway["spec"].get("listeners", []))},
            "deploymentOwnedManagers": managers, "routes": routes, "http01Certificates": certificates, "certificateFindings": certificate_findings,
            "webhookEndpointInventory": endpoint_inventory,
            "leaderObservation": leader_observation(leases[0], managers, now) if len(leases) == 1 else {"finding": "manager leader lease absent", "limit": "Inventory only; leader election may be disabled or configured differently"},
            "attachmentSelectionLimit": "Shared Gateway spec references, including inventoried ListenerSet/XListenerSet chains. Missing listener parents are findings; other Gateways are outside this audit target. Does not establish accepted attachment, allowedRoutes or serving paths",
            "attachmentFindings": attachment_findings,
            "attachmentCoverage": {resource: "inspected" if resource in installed else "not installed" for resource in attachment_resources},
            "candidatePinLimit": "Candidate pins follow inventory and current Project membership only; they are not reviewed namespace authorization or producer provenance",
            "candidateNativeRouteNamespaceUIDs": {ns: namespaces[ns]["metadata"]["uid"] for ns in sorted(native_namespaces)},
            "candidateACMENamespaceUIDs": {ns: namespaces[ns]["metadata"]["uid"] for ns in sorted(acme_namespaces)},
            "subnets": subnets}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    report = audit(args.kubeconfig)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"output": str(args.output), "sharedRoutes": len(report["routes"]), "http01Certificates": len(report["http01Certificates"]), "guardInstalled": report["guardInstalled"], "productionQualified": False}))


if __name__ == "__main__":
    main()
