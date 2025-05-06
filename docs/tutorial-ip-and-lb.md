# Managing IPs and Load Balancers

This tutorial walks you through managing External IPs (EIPs), Floating IPs (FIPs), Load Balancers, and deploying namespace-scoped Ingress controllers in Kube-DC using kubectl with YAML manifests.

## Prerequisites

Before starting this tutorial, ensure you have:

- Access to a Kube-DC cluster
- The `kubectl` command-line tool installed
- Helm installed (for Ingress controller deployment)
- A project with the necessary permissions to create network resources

## Understanding Network Resources in Kube-DC

Kube-DC's networking is built on Kube-OVN and includes several key components:

1. **External IP (EIP)**: Public IP addresses that provide connectivity from the internet to resources within Kube-DC
2. **Floating IP (FIP)**: Maps an internal IP address (of a VM or pod) to an External IP
3. **Service LoadBalancer**: Creates and maps an EIP to a service that routes traffic to pods or VMs
4. **Ingress Controller**: Provides HTTP/HTTPS routing to services within a namespace

## Managing External IPs (EIPs)

Each project in Kube-DC automatically receives a default EIP that acts as a NAT gateway for outbound traffic. You can also create additional EIPs for specific services.

### EIP Allocation Algorithm

When an EIP is created, Kube-DC follows a specific algorithm to allocate the external subnet:

1. **Check Default Subnet Compatibility**
   - The system first checks if the EIP's required external network type matches the default external subnet type
   - If they match, it looks for a free OEIP in that subnet
   - If a free OEIP is found, it's connected to the EIP
   - If no free OEIP exists, a new one is created

2. **Check Connected Subnets**
   - If the required network type is different from the default, the system takes a list of external subnets already connected to the project's VPC
   - It retrieves all free OEIPs from these connected subnets
   - If at least one free OEIP is found, it's connected to the EIP

3. **Select Best Available Subnet**
   - If no connected subnets have free IPs, the system takes a complete list of available external networks
   - Networks are sorted by the number of available IPs (descending order)
   - The network with the most free addresses is selected

4. **Connect New Subnet to VPC**
   - The selected subnet is connected to the project's VPC
   - The system waits for the OEIP resource to be created
   - Once created, the OEIP is connected to the EIP

5. **Error Handling**
   - If no networks with free IPs are available, the operation fails with an error

This algorithm ensures optimal IP address utilization while providing the flexibility to support different external network types.

### Creating an EIP Using kubectl

For automation or GitOps workflows, you can create EIPs using kubectl and YAML manifests.

```yaml
apiVersion: kube-dc.com/v1
kind: EIp
metadata:
  name: web-server-eip
  namespace: shalb-demo
spec: {}
```

Apply this manifest:

```bash
kubectl apply -f eip.yaml
```

Check the status:

```bash
kubectl get eip -n shalb-demo
```

## Managing Floating IPs (FIPs)

Floating IPs map an internal IP address (of a VM or pod) to an External IP, enabling direct access to specific resources.

### Creating a FIP Using kubectl

Create a FIP manifest:

```yaml
apiVersion: kube-dc.com/v1
kind: FIp
metadata:
  name: database-vm-fip
  namespace: shalb-demo
spec:
  ipAddress: 10.0.10.171  # Internal IP of your VM or pod
  eip: web-server-eip     # Name of an existing EIP
```

Apply this manifest:

```bash
kubectl apply -f fip.yaml
```

Check the status:

```bash
kubectl get fip -n shalb-demo
```

## Configuring Load Balancers

Load Balancers in Kube-DC are implemented as Kubernetes Services of type LoadBalancer, with specific annotations to control their behavior.

### Creating a Load Balancer for Pods Using kubectl

Create a Service manifest:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nginx-service-lb
  namespace: shalb-demo
  annotations:
    service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"  # Use project's default EIP
    # service.nlb.kube-dc.com/bind-on-eip: "web-server-eip"  # Or use a dedicated EIP
spec:
  type: LoadBalancer
  selector:
    app: nginx
  ports:
    - name: http
      protocol: TCP
      port: 80
      targetPort: 80
    - name: https
      protocol: TCP
      port: 443
      targetPort: 443
```

Apply this manifest:

```bash
kubectl apply -f service-lb.yaml
```

### Creating a Load Balancer for VM SSH Access

You can also create a Load Balancer to expose SSH access to a virtual machine:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: vm-ssh
  namespace: shalb-demo
  annotations:
    service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
spec:
  type: LoadBalancer
  selector:
    vm.kubevirt.io/name: debian  # Target VM name
  ports:
    - name: ssh
      protocol: TCP
      port: 2222  # External port
      targetPort: 22  # Internal port (SSH)
```

Apply this manifest:

```bash
kubectl apply -f vm-ssh-lb.yaml
```

## Managing Network Resources with kubectl

Check the status of network resources:

```bash
# List all External IPs
kubectl get eip -n shalb-demo

# List all Floating IPs
kubectl get fip -n shalb-demo

# Get details about a specific LoadBalancer service
kubectl describe service nginx-service-lb -n shalb-demo

# Check if your LoadBalancer has an external IP assigned
kubectl get service -n shalb-demo
```

## Deploying Namespace-Scoped Ingress Controllers

For more advanced HTTP/HTTPS routing capabilities, you can deploy an ingress-nginx controller scoped to your specific namespace. This allows you to have complete control over the Ingress resources in your project.

### Understanding Namespace-Scoped Ingress

A namespace-scoped ingress controller:
- Only watches for Ingress resources in the specified namespace
- Doesn't interfere with other controllers in the cluster
- Uses your project's networking resources (like the default EIP)
- Provides advanced routing, SSL termination, and load balancing

### Deploying ingress-nginx Controller with Helm

#### Step 1: Create a values.yaml file for your configuration

```yaml
controller:
  ingressClassResource:
    enabled: false  # Disables the default IngressClass creation
  ingressClass: ""  # No default IngressClass
  scope:
    enabled: true  # Enables namespace-scoped mode
    namespace: shalb-demo  # Restricts the controller to this namespace
  watchIngressWithoutClass: false
  admissionWebhooks:
    enabled: false
  service:
    annotations:
      service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"
rbac:
  create: true
  scope: true

defaultBackend:
  enabled: false  # Disables the default backend
```

Save this as `ingress-values.yaml`.

#### Step 2: Add the ingress-nginx Helm repository (if not already added)

```bash
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo update
```

#### Step 3: Install the ingress-nginx controller

```bash
helm upgrade --install ingress ingress-nginx/ingress-nginx \
  --namespace shalb-demo \
  --values ingress-values.yaml
```

#### Step 4: Verify the installation

```bash
kubectl get pods -n shalb-demo -l app.kubernetes.io/name=ingress-nginx
kubectl get svc -n shalb-demo -l app.kubernetes.io/name=ingress-nginx
```

The controller pod should be running, and the service should have an external IP assigned.

### Creating an Ingress Resource

Once your ingress controller is running, you can create Ingress resources to route traffic to your services:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: example-ingress
  namespace: shalb-demo
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /
spec:
  rules:
  - host: example.kube-dc.com  # Replace with your domain
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: nginx-service
            port:
              number: 80
```

Apply this manifest:

```bash
kubectl apply -f example-ingress.yaml
```

### Configuring SSL/TLS with cert-manager

For secure HTTPS connections, you can deploy cert-manager to automatically obtain and manage certificates:

#### Step 1: Install cert-manager in your namespace

```bash
helm repo add jetstack https://charts.jetstack.io
helm repo update

helm install cert-manager jetstack/cert-manager \
  --namespace shalb-demo \
  --set installCRDs=true \
  --set namespace=shalb-demo
```

#### Step 2: Create an Issuer or ClusterIssuer

For Let's Encrypt:

```yaml
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: letsencrypt-prod
  namespace: shalb-demo
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: your-email@example.com
    privateKeySecretRef:
      name: letsencrypt-prod
    solvers:
    - http01:
        ingress:
          class: nginx
```

Apply this manifest:

```bash
kubectl apply -f issuer.yaml
```

#### Step 3: Update your Ingress with TLS configuration

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: example-ingress
  namespace: shalb-demo
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /
    cert-manager.io/issuer: "letsencrypt-prod"
spec:
  tls:
  - hosts:
    - example.kube-dc.com
    secretName: example-tls
  rules:
  - host: example.kube-dc.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: nginx-service
            port:
              number: 80
```

Apply this manifest:

```bash
kubectl apply -f example-ingress-tls.yaml
```

## Putting It All Together: Exposing a Web Application

Let's walk through a complete example of deploying a web application and exposing it to the internet:

### Step 1: Deploy an Nginx Pod

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
  namespace: shalb-demo
spec:
  replicas: 2
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:latest
        ports:
        - containerPort: 80
```

Apply this manifest:

```bash
kubectl apply -f nginx-deployment.yaml
```

### Step 2: Create a Service for the Deployment

```yaml
apiVersion: v1
kind: Service
metadata:
  name: nginx-service
  namespace: shalb-demo
spec:
  selector:
    app: nginx
  ports:
    - name: http
      protocol: TCP
      port: 80
      targetPort: 80
```

Apply this manifest:

```bash
kubectl apply -f nginx-service.yaml
```

### Step 3: Deploy a Namespace-Scoped Ingress Controller

Follow the steps in the "Deploying Namespace-Scoped Ingress Controllers" section to deploy your ingress controller.

### Step 4: Create an Ingress Resource

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: nginx-ingress
  namespace: shalb-demo
spec:
  rules:
  - host: nginx.example.com  # Replace with your domain
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: nginx-service
            port:
              number: 80
```

Apply this manifest:

```bash
kubectl apply -f nginx-ingress.yaml
```

### Step 5: Verify Access

Get the external IP of your ingress controller's service:

```bash
kubectl get svc -n shalb-demo -l app.kubernetes.io/name=ingress-nginx
```

Configure your DNS to point your domain (e.g., nginx.example.com) to this IP address. Once DNS propagates, you should be able to access your Nginx service using the domain.

## Best Practices

1. **Resource Naming**: Use descriptive names for your network resources
2. **EIP Conservation**: When possible, use the project's default EIP with annotations rather than creating dedicated EIPs
3. **Security**: Limit exposed ports to only what's necessary and use HTTPS with valid certificates
4. **Monitoring**: Regularly check the status of your network resources
5. **Documentation**: Document which services are exposed on which domains/paths

## Troubleshooting

### Common Issues

1. **EIP Not Allocated**: EIP creation may take a few moments. Check status with `kubectl get eip`
2. **LoadBalancer Pending**: External IP allocation may take time. Check with `kubectl describe service`
3. **Cannot Connect to Service**: Verify that the service's selector matches your pod labels
4. **Ingress Not Routing Traffic**: Check ingress controller logs and ingress resource status

### Debugging Commands

```bash
# Check EIP status and details
kubectl describe eip web-server-eip -n shalb-demo

# Check FIP status and details
kubectl describe fip database-vm-fip -n shalb-demo

# Check LoadBalancer service events
kubectl describe service nginx-service-lb -n shalb-demo

# Check if pods are selected by the service
kubectl get pods -l app=nginx -n shalb-demo

# Check ingress controller logs
kubectl logs -n shalb-demo -l app.kubernetes.io/name=ingress-nginx

# Check ingress status
kubectl describe ingress nginx-ingress -n shalb-demo
```

## Summary

In this tutorial, you've learned how to manage External IPs (EIPs), Floating IPs (FIPs), LoadBalancer services, and namespace-scoped Ingress controllers in Kube-DC. These networking resources provide flexible options for exposing your applications and VMs to external traffic, with Ingress controllers offering advanced HTTP/HTTPS routing capabilities for your web applications.
