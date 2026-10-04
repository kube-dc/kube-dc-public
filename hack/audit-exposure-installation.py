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


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def identity(obj):
    meta = obj["metadata"]
    return {"kind": obj.get("kind"), "namespace": meta.get("namespace"),
            "name": meta["name"], "uid": meta["uid"]}


def controller_owner(obj, kind):
    return next((owner for owner in obj["metadata"].get("ownerReferences", [])
                 if owner.get("controller") and owner["kind"] == kind), None)


def targets_gateway(route):
    parents = list(route.get("spec", {}).get("parentRefs", []))
    if route.get("spec", {}).get("parentRef"):
        parents.append(route["spec"]["parentRef"])
    for parent in parents:
        if (parent.get("namespace", route["metadata"]["namespace"]), parent.get("name")) == GATEWAY and parent.get("kind", "Gateway") == "Gateway" and parent.get("group", "gateway.networking.k8s.io") == "gateway.networking.k8s.io":
            return True
    return False


def audit(kubeconfig):
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
    for resource in attachment_resources:
        if resource not in installed:
            continue
        for route in items(resource):
            if not targets_gateway(route):
                continue
            meta = route["metadata"]
            owner = controller_owner(route, "Service")
            service = services.get((meta["namespace"], owner["name"])) if owner else None
            owned = bool(owner and owner["apiVersion"] == "v1" and service and owner["uid"] == service["metadata"]["uid"])
            rules = route["spec"].get("rules", [])
            routes.append({**identity(route), "specSHA256": digest(route["spec"]),
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
    replica_sets = {rs["metadata"]["uid"] for rs in items("replicasets.apps") if any(o["uid"] == manager["metadata"]["uid"] and o.get("controller") for o in rs["metadata"].get("ownerReferences", []))}
    managers = []
    for pod in items("pods"):
        if not any(o["uid"] in replica_sets and o.get("controller") for o in pod["metadata"].get("ownerReferences", [])):
            continue
        managers.append({**identity(pod), "ready": any(c["type"] == "Ready" and c["status"] == "True" for c in pod.get("status", {}).get("conditions", [])),
                         "deleting": bool(pod["metadata"].get("deletionTimestamp")),
                         "imageIDs": [c.get("imageID") for c in pod.get("status", {}).get("containerStatuses", [])],
                         "exposureEnv": {e["name"]: e.get("value") for container in pod["spec"]["containers"] for e in container.get("env", []) if e["name"].startswith(("EXPOSURE_", "FIP_SOURCE_POLICY_"))}})
    helm = get("helmreleases.helm.toolkit.fluxcd.io", "kube-dc", "kube-dc")
    values = helm["spec"].get("values", {})
    available = values.get("backend", {}).get("exposureSourcePolicy", {})
    native_namespaces = {route["namespace"] for route in routes if route["class"] == "non-Project-native"}
    subnets = [{**identity(subnet), "cidr": subnet["spec"].get("cidrBlock"), "availableIPv4": subnet.get("status", {}).get("v4availableIPs")} for subnet in items("subnets.kubeovn.io") if subnet["metadata"]["name"] in ("ext-public", "ext-cloud")]
    return {"auditedAtUTC": datetime.datetime.now(datetime.timezone.utc).isoformat(), "readOnly": True,
            "productionQualified": False, "evidenceLimit": "GET inventory; no authenticated admission, traffic, renewal, or lifecycle qualification",
            "helmGeneration": helm["metadata"]["generation"], "chart": helm["spec"]["chartRef"]["name"] if "chartRef" in helm["spec"] else helm["spec"]["chart"]["spec"]["version"],
            "consoleCapabilities": available, "guardConfigured": values.get("manager", {}).get("webhook", {}).get("protectExposureRoutes", False),
            "guardInstalled": any(w["name"] == "vexposurerouteclaims.kube-dc.com" for config in items("validatingwebhookconfigurations") for w in config.get("webhooks", [])),
            "gateway": {**identity(gateway), "specSHA256": digest(gateway["spec"]), "listenerCount": len(gateway["spec"].get("listeners", []))},
            "deploymentOwnedManagers": managers, "routes": routes, "http01Certificates": certificates, "certificateFindings": certificate_findings,
            "attachmentSelectionLimit": "Direct Gateway parent references only; ListenerSet/XListenerSet transitive route attachments are not resolved",
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
