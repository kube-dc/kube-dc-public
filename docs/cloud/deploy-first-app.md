# Deploy Your First K8s Application

This guide walks you through deploying a production-ready WordPress site on Kube-DC using Helm and exposing it to the internet with automatic HTTPS — all in under 5 minutes.

## Prerequisites

- A Kube-DC Cloud [project](first-project.md) with `egressNetworkType: cloud`
- [CLI access](cli-kubeconfig.md) configured — `kubectl` working against your project
- [Helm](https://helm.sh/docs/intro/install/) installed locally

Verify your setup:

```bash
# Confirm you're connected to the right project
kubectl get ns
```

---

## Step 1: Create a TLS Certificate Issuer

Before deploying, set up a Let's Encrypt issuer so your app gets a free HTTPS certificate automatically.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: your-email@example.com
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
    - http01:
        gatewayHTTPRoute:
          parentRefs:
          - group: gateway.networking.k8s.io
            kind: Gateway
            name: eg
            namespace: envoy-gateway-system
EOF
```

:::note One-time setup
You only need to create the Issuer once per project. All services in the project can reuse it.
:::

---

## Step 2: Install WordPress with Helm

Create a `values.yaml` file for the Helm chart:

```yaml
service:
  type: LoadBalancer
  annotations:
    service.nlb.kube-dc.com/expose-route: "https"

networkPolicy:
  enabled: false

mariadb:
  networkPolicy:
    enabled: false
```

:::tip Hostname Pattern
The auto-generated hostname follows: `<service-name>-<namespace>.<domain>`

Since project namespaces use the format `<org>-<project>`, the full hostname becomes:
- Organization: `acme`, Project: `demo`
- Service: `wordpress`, Namespace: `acme-demo`
- Hostname: `wordpress-acme-demo.kube-dc.cloud`
:::

Install WordPress using the Bitnami Helm chart:

```bash
helm install wordpress oci://registry-1.docker.io/bitnamicharts/wordpress \
  --values values.yaml
```

Wait for the pods to become ready:

```bash
kubectl get pods -w
```

You should see two pods running — `wordpress` and `wordpress-mariadb`:

```
NAME                         READY   STATUS    RESTARTS   AGE
wordpress-6b4c8f9d7b-x2k5l  1/1     Running   0          90s
wordpress-mariadb-0          1/1     Running   0          90s
```

---

## Step 3: Access Your WordPress Site

The `expose-route: https` annotation automatically:
1. Allocates an EIP for the LoadBalancer service
2. Creates a Gateway HTTPS route
3. Provisions a Let's Encrypt TLS certificate
4. Assigns a default hostname

Check the assigned hostname:

```bash
kubectl get svc wordpress -o jsonpath='{.metadata.annotations.service\.nlb\.kube-dc\.com/route-hostname-status}'
```

The output will be your site's URL, for example:

```
wordpress-acme-demo.kube-dc.cloud
```

Open it in your browser:

```
https://wordpress-acme-demo.kube-dc.cloud
```

:::tip Certificate provisioning
The TLS certificate may take 1–2 minutes to be issued. You can check its status with:
```bash
kubectl get certificate
```
:::

---

## Step 4: Log in to WordPress Admin

Retrieve the auto-generated admin password:

```bash
echo "Username: user"
echo "Password: $(kubectl get secret wordpress -o jsonpath='{.data.wordpress-password}' | base64 -d)"
```

Navigate to `https://<your-hostname>/wp-admin` to access the WordPress dashboard.

---

## What Just Happened?

With two commands (`kubectl apply` + `helm install`) you deployed a full WordPress stack with:

- **WordPress** application server
- **MariaDB** database with persistent storage
- **LoadBalancer** service with a dedicated external IP
- **HTTPS** with an auto-provisioned Let's Encrypt certificate
- **Public hostname** on the default `kube-dc.cloud` domain

All of this runs inside your isolated project namespace with network-level separation from other tenants.

---

## Clean Up

To remove the WordPress deployment and its persistent data:

```bash
helm uninstall wordpress
kubectl delete pvc data-wordpress-mariadb-0 wordpress
```

:::note
Helm does not delete PersistentVolumeClaims on uninstall. The `kubectl delete pvc` command removes the MariaDB and WordPress storage volumes so a fresh reinstall starts clean.
:::

---

## Next Steps

- [Service Exposure Guide](service-exposure.md) — Learn about custom domains, TLS passthrough, and EIP-based exposure
- [Virtual Machines](creating-vm.md) — Deploy VMs alongside containers
- [Public & Floating IPs](public-floating-ips.md) — Manage IP addresses
- [Object Storage](object-storage.md) — Add S3-compatible storage to your apps
