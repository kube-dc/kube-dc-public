---
name: create-project
description: Create a new Kube-DC project with isolated VPC networking inside an existing organization. Handles network type selection (cloud vs public), organization verification, and project manifest generation.
---

## Prerequisites
- Organization must exist and be Ready
- User must have admin access to the organization
- Check org quota for project limits before creating

## Steps

### 1. Verify Organization Exists

```bash
kubectl get organization {org-name} -n {org-name}
```

The organization namespace is the same as the organization name.

### 2. Choose Network Type

| Type | When to Use |
|------|-------------|
| `cloud` (recommended) | Web apps, APIs, microservices — shared NAT gateway, more secure |
| `public` | Game servers, direct IP needs — dedicated public gateway IP |

Default to `cloud` unless the user explicitly needs dedicated public IPs.

### 3. Apply Project Manifest

```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: {project-name}
  namespace: {org-name}
spec:
  egressNetworkType: cloud    # or: public
  description: "{description}"
```

See @project-template.yaml for the full template.

### 4. Wait for Ready

```bash
kubectl get project {project-name} -n {org-name} -w
```

The project creates namespace `{org-name}-{project-name}` with:
- Isolated VPC subnet (Kube-OVN)
- Default network `{org-name}-{project-name}/default`
- SSH keypair secrets (`ssh-keypair-default`, `authorized-keys-default`)
- Default gateway EIP

### 5. Verify Resources

```bash
kubectl get project {project-name} -n {org-name}
kubectl get secret ssh-keypair-default -n {org-name}-{project-name}
kubectl get eip -n {org-name}-{project-name}
```

## Safety
- Always verify org exists before creating project
- Default to `cloud` network type
- Project names must be lowercase, alphanumeric with hyphens
- One project = one VPC = one subnet — this is the isolation boundary
