# Obtaining and Using Kubeconfig in Your Local Console

This guide explains how to obtain and configure a kubeconfig file for the kube-dc platform to use in your local development environment.

## Overview

The kubeconfig file is essential for authenticating with the Kubernetes API server. In kube-dc, authentication is handled through Keycloak, which provides secure token-based access.

This tutorial covers:
- Setting up the authentication script
- Generating a kubeconfig file
- Using kubeconfig with kubectl
- Troubleshooting common issues

## Prerequisites

Before you begin, ensure you have:

- Access to a kube-dc organization and project
- Your Keycloak username and password
- `kubectl` installed on your local machine
- `curl` and `jq` utilities installed (required for token operations)

## Using the Authentication Helper Script

kube-dc provides a helper script that simplifies the kubeconfig generation process.

### Step 1: Download and Run the Authentication Script

You can download the authentication script directly from the public repository:

```bash
# Create a directory for the script
mkdir -p ~/.kube-dc/bin

# Download the script
curl -o ~/.kube-dc/bin/kdc_get_kubeconfig.sh https://raw.githubusercontent.com/kube-dc/kube-dc-public/main/hack/auth/kdc_get_kubeconfig.sh

# Make it executable
chmod +x ~/.kube-dc/bin/kdc_get_kubeconfig.sh

# Run the authentication script with your organization and project name
~/.kube-dc/bin/kdc_get_kubeconfig.sh your-org/your-project
```

Alternatively, if you have the entire repository cloned:

```bash
# If you have the repository already cloned
cd kube-dc
./hack/auth/kdc_get_kubeconfig.sh your-org/your-project
```

The script will prompt you for the following information:
- Keycloak endpoint URL (e.g., `https://login.dev.kube-dc.com`)
- Organization name (your Keycloak realm)
- Kubernetes API server URL (e.g., `https://kube-api.dev.kube-dc.com:6443`)
- Cluster name (usually `kube-dc`)
- User name (your Keycloak username)
- Context name (usually `kube-dc`)
- CA certificate (you can provide this as a file, paste it directly, or skip for insecure mode)

### Step 2: Activate the Generated Configuration

After the script completes, activate the configuration:

```bash
source ~/.kube-dc/your-org-your-project-name/activate.sh
```

This will set the `KUBECONFIG` environment variable to point to your new configuration file.

### Step 3: Test Your Connection

Test that your kubeconfig works correctly:

```bash
kubectl get pods
```

On first use, you'll be prompted to enter your Keycloak username and password. The script will obtain tokens and cache them for subsequent commands.



## Troubleshooting

### Authentication Issues

If you're experiencing authentication problems:

1. **Token expiration**: The refresh token may have expired. Delete the `.refresh_token` file in your kubeconfig directory and try again:
   ```bash
   rm ~/.kube-dc/*-*/scripts/.refresh_token
   ```

2. **Invalid credentials**: Ensure you're using the correct username and password for your Keycloak account.

3. **Connection issues**: Verify your network can reach the Keycloak and API servers.

### Permission Issues

If you can authenticate but receive permission errors:

1. **Namespace access**: Ensure you're using the correct namespace in your context. Your namespace should be in the format `organization-project`.

2. **Role assignment**: Contact your organization administrator to verify you have the appropriate roles assigned in Keycloak.

3. **Resource-specific permissions**: Check that your role has permissions for the specific resources you're trying to access.

## Security Considerations

- Keep your kubeconfig file secure (600 permissions)
- Never share your refresh or access tokens
- Be cautious when using `insecure-skip-tls-verify: true` in production environments
- If your credentials may be compromised, contact your administrator to revoke your tokens

## Next Steps

- [User and Group Management](user-groups.md): Learn about role-based access control
- [Tutorial: Virtual Machines](tutorial-virtual-machines.md): Deploy your first VM
- [Examples](../examples/): Explore example manifests for various resources
