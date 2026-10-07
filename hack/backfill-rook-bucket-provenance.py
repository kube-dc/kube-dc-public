#!/usr/bin/env python3
"""Backfill services.kube-dc.com/provenance=rook-ceph on legacy Rook bucket Secrets.

The managed-children webhook stamps the provenance label on objects that the
rook-ceph service accounts CREATE in Project namespaces. Bucket Secrets created
before that webhook existed never got it. Two policies then treat them as
foreign: the PostgreSQL startup webhook refuses them as backup credentials, and
the managed-services policy lets Rook rewrite a Project Secret only while it
carries Rook's own provenance.

This script labels a Secret only when every check below holds. The label is an
attestation, so a Secret that fails any check is reported and left alone:

  * it is in a Project namespace (label kube-dc.com/project);
  * it carries the Rook bucket-provisioner label and no provenance label yet;
  * its only controller is an ObjectBucketClaim of the same name, live, Bound,
    with the same UID;
  * it holds exactly the S3 key pair Rook writes;
  * every recorded writer is Rook's provisioner ("rook");
  * it was created within --max-skew seconds after its claim;
  * it was created before --cutoff, when automatic stamping began (server-set
    timestamps; a Secret Rook created later would already be labelled).

Dry run by default. --apply labels each Secret with a resourceVersion
precondition, so a Secret that changed since it was checked is not touched.

    hack/backfill-rook-bucket-provenance.py --kubeconfig K            # report
    hack/backfill-rook-bucket-provenance.py --kubeconfig K --apply    # label
"""
import argparse
import datetime
import json
import subprocess
import sys

PROVENANCE = "services.kube-dc.com/provenance"
PROVISIONER = ("bucket-provisioner", "rook-ceph.ceph.rook.io-bucket")
S3_KEYS = {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}


def kubectl(kubeconfig, *args):
    return subprocess.run(["kubectl", "--kubeconfig", kubeconfig, *args], capture_output=True, text=True, timeout=60)


def get(kubeconfig, *args):
    result = kubectl(kubeconfig, "get", *args, "-o", "json", "--show-managed-fields")
    if result.returncode != 0:
        raise RuntimeError(result.stderr.strip())
    return json.loads(result.stdout)


def ts(value):
    return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))


def check(secret, claims, cutoff, max_skew):
    meta = secret["metadata"]
    controllers = [o for o in meta.get("ownerReferences", []) if o.get("controller")]
    if len(controllers) != 1:
        return "not controlled by exactly one owner"
    ref = controllers[0]
    if ref.get("apiVersion") != "objectbucket.io/v1alpha1" or ref.get("kind") != "ObjectBucketClaim" or ref.get("name") != meta["name"]:
        return "controller is not its own ObjectBucketClaim"
    claim = claims.get(meta["name"])
    if not claim or claim["metadata"]["uid"] != ref.get("uid"):
        return "ObjectBucketClaim missing or UID differs"
    if claim["metadata"].get("deletionTimestamp") or claim.get("status", {}).get("phase") != "Bound":
        return "ObjectBucketClaim is not live and Bound"
    if set((secret.get("data") or {}).keys()) != S3_KEYS:
        return "data is not exactly the S3 key pair"
    writers = {f.get("manager") for f in meta.get("managedFields", [])}
    if writers != {"rook"}:
        return f"writers {sorted(writers)} are not only rook"
    created, claimed = ts(meta["creationTimestamp"]), ts(claim["metadata"]["creationTimestamp"])
    if not (claimed <= created <= claimed + datetime.timedelta(seconds=max_skew)):
        return "not created within the skew after its claim"
    if created >= cutoff:
        return "created after automatic stamping began; Rook would have labelled it"
    return None


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--kubeconfig", required=True)
    parser.add_argument("--apply", action="store_true", help="label the Secrets that pass every check")
    parser.add_argument("--cutoff", default="2026-10-04T00:00:00Z", help="automatic stamping start (UTC)")
    parser.add_argument("--max-skew", type=int, default=300, help="seconds between claim and Secret creation")
    args = parser.parse_args()
    cutoff = ts(args.cutoff)

    projects = {n["metadata"]["name"] for n in get(args.kubeconfig, "namespaces", "-l", "kube-dc.com/project")["items"]}
    secrets = get(args.kubeconfig, "secrets", "-A", "-l", f"{PROVISIONER[0]}={PROVISIONER[1]}")["items"]
    candidates = [s for s in secrets if s["metadata"]["namespace"] in projects and PROVENANCE not in (s["metadata"].get("labels") or {})]
    claims_by_ns = {}
    report = {"mode": "apply" if args.apply else "dry-run", "labelled": [], "eligible": [], "refused": []}
    for secret in sorted(candidates, key=lambda s: (s["metadata"]["namespace"], s["metadata"]["name"])):
        ns, name = secret["metadata"]["namespace"], secret["metadata"]["name"]
        if ns not in claims_by_ns:
            claims_by_ns[ns] = {c["metadata"]["name"]: c for c in get(args.kubeconfig, "objectbucketclaims.objectbucket.io", "-n", ns)["items"]}
        reason = check(secret, claims_by_ns[ns], cutoff, args.max_skew)
        key = f"{ns}/{name}"
        if reason:
            report["refused"].append({"secret": key, "reason": reason})
            continue
        if not args.apply:
            report["eligible"].append(key)
            continue
        result = kubectl(args.kubeconfig, "-n", ns, "label", "secret", name, f"{PROVENANCE}=rook-ceph",
                         f"--resource-version={secret['metadata']['resourceVersion']}")
        if result.returncode != 0:
            report["refused"].append({"secret": key, "reason": "label failed: " + result.stderr.strip()})
        else:
            report["labelled"].append(key)
    json.dump(report, sys.stdout, indent=2)
    print()
    return 1 if report["refused"] and args.apply else 0


if __name__ == "__main__":
    sys.exit(main())
