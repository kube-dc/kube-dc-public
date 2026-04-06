---
name: ssh-into-vm
description: SSH into a Kube-DC virtual machine using the project's auto-generated SSH keypair. Covers extracting keys, finding VM IP, and connecting via internal IP or external FIP/EIP.
---

## Prerequisites
- VM must be running with `qemu-guest-agent` active
- Project namespace: `{org}-{project}`

## Steps

### 1. Extract SSH Private Key

```bash
kubectl get secret ssh-keypair-default -n {project-namespace} \
  -o jsonpath='{.data.id_rsa}' | base64 -d > /tmp/vm_ssh_key
chmod 600 /tmp/vm_ssh_key
```

### 2. Get VM IP Address

**Internal IP** (from VMI status):
```bash
kubectl get vmi {vm-name} -n {project-namespace} \
  -o jsonpath='{.status.interfaces[0].ipAddress}'
```

**Floating IP** (if FIP exists):
```bash
kubectl get fip -n {project-namespace}
```

**EIP + LoadBalancer** (if SSH service exists):
```bash
kubectl get svc {vm-name}-ssh -n {project-namespace} \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}'
```

### 3. SSH In

```bash
ssh -i /tmp/vm_ssh_key {os-user}@{ip-address}
```

### Default OS Users

| OS | User |
|----|------|
| Ubuntu | `ubuntu` |
| Debian | `debian` |
| Windows | `kube-dc` |

### 4. Clean Up

```bash
rm /tmp/vm_ssh_key
```

## How SSH Keys Work

1. When a project is created, Kube-DC generates an RSA-2048 keypair
2. Private key → `ssh-keypair-default` secret (keys: `id_rsa`, `id_rsa.pub`)
3. Public key → `authorized-keys-default` secret (key: `admin`)
4. VMs reference `authorized-keys-default` via `accessCredentials`
5. QEMU guest agent injects the public key into `~/.ssh/authorized_keys`

## Adding Custom SSH Keys

```bash
# Add your own public key to the authorized-keys secret
kubectl edit secret authorized-keys-default -n {project-namespace}
# Add a new data key with your base64-encoded public key
```

Or override `accessCredentials` in the VM manifest to point to a custom secret.

## Verification

After SSH connection:

```bash
# 1. Verify SSH key was extracted (before connecting)
test -f /tmp/vm_ssh_key && echo "Key exists" || echo "Key missing"
ls -la /tmp/vm_ssh_key
# Expected: -rw------- (600 permissions)

# 2. Verify VM has IP and guest agent is running
kubectl get vmi {vm-name} -n {project-namespace} -o jsonpath='{.status.interfaces[0].ipAddress}'
# Expected: non-empty IP address

kubectl get vmi {vm-name} -n {project-namespace} -o jsonpath='{.status.conditions[?(@.type=="AgentConnected")].status}'
# Expected: True

# 3. Test SSH connectivity (non-interactive)
ssh -i /tmp/vm_ssh_key -o StrictHostKeyChecking=no -o ConnectTimeout=5 {os-user}@{ip} echo "SSH OK"
# Expected: "SSH OK"
```

**Success**: Key extracted, VM has IP, AgentConnected=True, SSH returns output.
**Failure**:
- "Permission denied": wrong OS user or key not injected yet (wait for guest agent)
- "Connection timed out": VM IP not reachable (internal IP only reachable from cluster)
- No IP reported: guest agent not installed — check cloud-init
## Safety
- Never expose the private key contents in chat output
- Use temporary files with `chmod 600`
- Clean up key files after use
- Internal IPs only reachable from within the cluster or via kubectl proxy
