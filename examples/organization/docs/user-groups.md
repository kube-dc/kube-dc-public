# Kube-DC User and Group Management Guide

This guide explains how to set up and manage users, groups, and roles in Kube-DC using Kubernetes RBAC and Keycloak integration.

## Table of Contents
- [Overview](#overview)
- [Prerequisites](#prerequisites)
- [Step-by-Step Guide](#step-by-step-guide)
  - [1. Creating Project Roles](#1-creating-project-roles)
  - [2. Creating Organization Groups](#2-creating-organization-groups)
  - [3. Managing Users in Keycloak](#3-managing-users-in-keycloak)
  - [4. Accessing Kube-DC UI](#4-accessing-kube-dc-ui)

## Overview

Kube-DC implements a multi-tenant access control system that combines:
- Kubernetes RBAC for resource-level permissions
- Organization Groups for project-level access management
- Keycloak for user authentication and group management

## Prerequisites

- Access to Kube-DC cluster with administrative privileges
- `kubectl` configured to access your cluster
- Access to Keycloak admin console

## Step-by-Step Guide

### 1. Creating Project Roles

Create a Kubernetes Role to define permissions within a project namespace.

```yaml
apiVersion: rbac.k8s.io/v1
kind: Role
metadata:
  namespace: shalb-demo  # Replace with your project namespace
  name: resource-manager
rules:
  - apiGroups: [""]  # "" indicates the core API group
    resources: ["pods", "services"]
    verbs: ["get", "list", "create", "watch", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments", "daemonsets", "replicasets"]
    verbs: ["get", "list", "create", "watch", "delete"]
```

Apply the role to your namespace using `kubectl apply -f role.yaml`

### 2. Creating Organization Groups

Create an OrganizationGroup CRD to define group permissions across projects.

```yaml
apiVersion: kube-dc.com/v1
kind: OrganizationGroup
metadata:
  name: "app-manager"
  namespace: shalb
spec:
  permissions:
  - project: "demo"
    roles:
    - resource-manager
  # Additional projects and roles can be added:
  # - project: "prod"
  #   roles:
  #   - resource-manager
```

### 3. Managing Users in Keycloak

#### 3.1. Access Keycloak Admin Console

Retrieve Keycloak access credentials:

```bash
kubectl get secret realm-access -n shalb -o jsonpath='{.data.url}' | base64 -d
kubectl get secret realm-access -n shalb -o jsonpath='{.data.user}' | base64 -d
kubectl get secret realm-access -n shalb -o jsonpath='{.data.password}' | base64 -d
```

#### 3.2. Create and Configure Users

1. Log in to the Keycloak admin console using the retrieved credentials
2. Navigate to Users → Add User
3. Fill in the required user information
4. Set up initial password in the Credentials tab
5. Add the user to the appropriate group (e.g., "app-manager")

### 4. Accessing Kube-DC UI

1. Navigate to the Kube-DC UI login page
2. Log in using the credentials created in Keycloak
3. Verify access to assigned project resources


