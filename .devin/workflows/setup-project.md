---
description: Create a new Kube-DC project with organization verification and optional resources
---

## Steps

1. Ask the user for: organization name, project name, network type (default: `cloud`), and optional description

2. Verify the organization exists:
```bash
kubectl get organization {org-name} -n {org-name}
```

3. Create the project:
```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: {project-name}
  namespace: {org-name}
spec:
  egressNetworkType: cloud
  description: "{description}"
```

4. Wait for the project to be Ready:
```bash
kubectl get project {project-name} -n {org-name} -w
```

5. Verify project resources were created:
```bash
kubectl get secret ssh-keypair-default -n {org-name}-{project-name}
kubectl get secret authorized-keys-default -n {org-name}-{project-name}
kubectl get eip -n {org-name}-{project-name}
```

6. Ask the user if they want to set up any of these optional resources:
   - cert-manager Issuer for HTTPS (recommended)
   - A managed database
   - A virtual machine

7. If Issuer requested, create it:
```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
  namespace: {org-name}-{project-name}
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@{org-name}.com
    privateKeySecretRef:
      name: letsencrypt-key
    solvers:
      - http01:
          ingress:
            ingressClassName: envoy
```

8. Report the project namespace (`{org-name}-{project-name}`) and available next steps.
