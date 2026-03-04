# GitOps & Automation

:::info Native Integration Coming
First-class ArgoCD and Flux CD integration is on the Kube-DC roadmap — with one-click install and automatic project discovery from the console. Until then, you can self-host either tool inside one of your projects today and use it to manage the rest of your workloads.
:::

GitOps is the practice of keeping all Kubernetes manifests in a Git repository and letting an automated operator apply changes to the cluster whenever you push. Kube-DC's fully standard Kubernetes API means any GitOps tool works out of the box.

---

## Architecture: GitOps Project

The recommended pattern is a dedicated project for your GitOps tooling that watches your other projects:

```
Git Repository
      │
      │  push / PR merge
      ▼
 ┌──────────────────────┐
 │  acme-gitops         │  ← dedicated GitOps project
 │  (ArgoCD or Flux)    │
 └──────────┬───────────┘
            │  deploys to
  ┌─────────┼──────────────┐
  ▼         ▼              ▼
acme-dev  acme-staging  acme-prod
```

All namespaces follow the `{org}-{project}` pattern (e.g. `acme-dev`). You create the `gitops` project from the Kube-DC console, install your GitOps tool into it, then grant it permission to reconcile your other project namespaces.

---

## Option 1: ArgoCD

### Install ArgoCD into a dedicated project

1. Create a project called `gitops` in the Kube-DC console

2. Install ArgoCD into that project's namespace:

```bash
# Replace 'acme' with your organization name
GITOPS_NS=acme-gitops

kubectl create namespace $GITOPS_NS --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -n $GITOPS_NS \
  -f https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml
```

3. Wait for ArgoCD to be ready:

```bash
kubectl wait --for=condition=available deployment/argocd-server \
  -n $GITOPS_NS --timeout=120s
```

4. Expose the ArgoCD UI via a LoadBalancer (or use port-forward for local access):

```bash
# Local access only
kubectl port-forward svc/argocd-server -n $GITOPS_NS 8080:443
```

5. Get the initial admin password:

```bash
kubectl -n $GITOPS_NS get secret argocd-initial-admin-secret \
  -o jsonpath="{.data.password}" | base64 -d
```

Open `https://localhost:8080` and log in with `admin` / the password above.

### Grant ArgoCD access to other projects

ArgoCD needs permission to create and manage resources in your other project namespaces. Create a ClusterRole and binding for each target project:

```bash
TARGET_NS=acme-prod   # repeat for each project you want ArgoCD to manage

kubectl create rolebinding argocd-manager \
  --clusterrole=admin \
  --serviceaccount=$GITOPS_NS:argocd-application-controller \
  -n $TARGET_NS
```

### Add your cluster to ArgoCD

```bash
# Install argocd CLI
curl -sSL -o /usr/local/bin/argocd \
  https://github.com/argoproj/argo-cd/releases/latest/download/argocd-linux-amd64
chmod +x /usr/local/bin/argocd

# Log in
argocd login localhost:8080 --username admin --insecure

# Register the in-cluster server (ArgoCD managing the same cluster it runs on)
argocd cluster add --in-cluster --name kube-dc
```

### Create an Application pointing at your Git repo

```bash
argocd app create my-app \
  --repo https://github.com/your-org/your-repo \
  --path manifests/prod \
  --dest-server https://kubernetes.default.svc \
  --dest-namespace acme-prod \
  --sync-policy automated \
  --auto-prune \
  --self-heal
```

Or declare the same thing as a YAML manifest (recommended — store this in Git too):

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-app
  namespace: acme-gitops
spec:
  project: default
  source:
    repoURL: https://github.com/your-org/your-repo
    targetRevision: main
    path: manifests/prod
  destination:
    server: https://kubernetes.default.svc
    namespace: acme-prod
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

```bash
kubectl apply -f app.yaml
```

Every push to `main` is now automatically applied to `acme-prod`.

---

## Option 2: Flux CD

Flux is a lightweight GitOps operator that runs as a set of controllers in your cluster. Unlike ArgoCD it has no UI — configuration is entirely declarative.

### Prerequisites

Install the Flux CLI:

```bash
curl -s https://fluxcd.io/install.sh | sudo bash
```

Verify:

```bash
flux --version
```

### Bootstrap Flux into a dedicated project

Flux bootstrap installs the controllers and commits its own manifests back to your repository:

```bash
GITOPS_NS=acme-gitops

# GitHub example
flux bootstrap github \
  --owner=your-org \
  --repository=your-repo \
  --branch=main \
  --path=clusters/kube-dc \
  --namespace=$GITOPS_NS \
  --personal   # remove this flag for org tokens
```

For GitLab:

```bash
flux bootstrap gitlab \
  --owner=your-group \
  --repository=your-repo \
  --branch=main \
  --path=clusters/kube-dc \
  --namespace=$GITOPS_NS
```

Flux will push a `clusters/kube-dc/` directory to your repo containing its own manifests and start watching that path.

### Grant Flux access to other projects

```bash
TARGET_NS=acme-prod   # repeat for each project

kubectl create rolebinding flux-manager \
  --clusterrole=admin \
  --serviceaccount=$GITOPS_NS:kustomize-controller \
  -n $TARGET_NS

kubectl create rolebinding flux-manager-helm \
  --clusterrole=admin \
  --serviceaccount=$GITOPS_NS:helm-controller \
  -n $TARGET_NS
```

### Declare a Kustomization targeting another project

Add this file to your Git repo under `clusters/kube-dc/`:

```yaml
# clusters/kube-dc/acme-prod.yaml
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: my-app
  namespace: acme-gitops
spec:
  interval: 1m
  url: https://github.com/your-org/your-repo
  ref:
    branch: main
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: my-app-prod
  namespace: acme-gitops
spec:
  interval: 5m
  sourceRef:
    kind: GitRepository
    name: my-app
  path: ./manifests/prod
  prune: true
  targetNamespace: acme-prod   # deploy into this project
```

Push this file to Git and Flux will reconcile `manifests/prod` into `acme-prod` within 1 minute.

### Check reconciliation status

```bash
flux get kustomizations -n acme-gitops
flux logs -n acme-gitops
```

---

## Typical Repository Layout

A clean layout for managing multiple Kube-DC projects from one repo:

```
your-repo/
├── clusters/
│   └── kube-dc/              # Flux bootstrap manifests (auto-generated)
├── manifests/
│   ├── dev/
│   │   ├── deployment.yaml
│   │   └── service.yaml
│   ├── staging/
│   │   └── kustomization.yaml
│   └── prod/
│       └── kustomization.yaml
└── base/
    ├── deployment.yaml        # shared base manifests
    └── kustomization.yaml
```

Use Kustomize overlays to keep environment-specific differences minimal:

```yaml
# manifests/prod/kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ../../base
patches:
  - patch: |-
      - op: replace
        path: /spec/replicas
        value: 3
    target:
      kind: Deployment
```

---

## CI/CD Integration

### GitHub Actions — build and let GitOps deploy

```yaml
# .github/workflows/build.yml
name: Build and Push
on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Build and push image
        run: |
          docker build -t registry.kube-dc.cloud/acme/my-app:${{ github.sha }} .
          docker push registry.kube-dc.cloud/acme/my-app:${{ github.sha }}
      - name: Update image tag in manifests
        run: |
          sed -i "s|image: .*|image: registry.kube-dc.cloud/acme/my-app:${{ github.sha }}|" \
            manifests/prod/deployment.yaml
          git config user.email "ci@acme.com"
          git config user.name "CI"
          git commit -am "ci: update image to ${{ github.sha }}"
          git push
```

The push triggers your GitOps controller (ArgoCD or Flux) to reconcile the new image tag into the cluster automatically.

---

## Resource Considerations

Both ArgoCD and Flux run as pods inside your `gitops` project and count toward your organization's quota:

| Tool | Approximate resource usage |
|------|---------------------------|
| ArgoCD (full install) | ~1 vCPU / 512 Mi baseline |
| Flux (controllers only) | ~200m CPU / 256 Mi baseline |

On **Dev Pool** (4 vCPU / 8 GB), Flux is the better fit. On **Pro Pool** or **Scale Pool**, either tool works comfortably alongside your workloads.

---

## Further Reading

- [ArgoCD documentation](https://argo-cd.readthedocs.io)
- [Flux CD documentation](https://fluxcd.io/flux)
- [Kustomize documentation](https://kustomize.io)
- [AI IDE Integration](ai-ide-integration.md) — use Claude Code or Cursor to write and apply manifests
- [CLI & Kubeconfig](cli-kubeconfig.md) — set up `kubectl` for Kube-DC
