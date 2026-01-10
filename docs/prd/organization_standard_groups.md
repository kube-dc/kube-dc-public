# PRD: Organization Standard Groups and Role Distribution

## Problem Statement

When approving join requests, the UI offers group options (`developer`, `project-manager`, `user`) that don't exist in the organization's Keycloak realm. Currently, only `org-admin` group is created during organization setup.

**Current State:**
- Organization controller creates only `org-admin` group in Keycloak realm
- RoleBinding maps `<org>:org-admin` to Kubernetes RBAC in org namespace
- No standard user-level groups exist for assigning regular users
- Join request approval fails silently when trying to add users to non-existent groups

## Design Approach: Hybrid Model

This PRD implements a **hybrid approach** combining automatic standard groups with fine-grained OrganizationGroup CRD control:

1. **Standard Groups** (`org-admin`, `user`) - Created automatically during organization setup, provide org-wide basic access
2. **Elevated Access** (`developer`, `project-manager`) - Granted via OrganizationGroup CRD for per-project granular control

## Requirements

### 1. Standard Keycloak Groups (Organization Realm)

Create during organization creation:

| Group Name | Purpose | Created By |
|------------|---------|------------|
| `org-admin` | Full organization management | Organization controller (exists) |
| `user` | Read-only access to all projects | Organization controller (new) |

**Note:** `developer` and `project-manager` groups are created on-demand via OrganizationGroup CRD for per-project access control.

### 2. Kubernetes Roles

#### Organization Namespace Roles

| Role Name | Permissions | Created By |
|-----------|-------------|------------|
| `{org}-admin` | Full access to org resources | Organization controller (exists) |
| `{org}-user` | Read-only org access | Organization controller (new) |

#### Project Namespace Roles (Templates)

Role templates stored in `kube-dc` namespace, copied to each project namespace:

| Role Name | Permissions | Created By |
|-----------|-------------|------------|
| `admin` | Full project access | Project controller (exists) |
| `developer` | VMs, containers, pods, services: full CRUD | Project controller (new) |
| `project-manager` | VMs, containers, pods, services: get, list, watch | Project controller (new) |
| `user` | VMs, containers, pods, services: get, list | Project controller (new) |

### 3. RoleBindings

#### Organization Namespace

| RoleBinding | Subject | Role |
|-------------|---------|------|
| `org-admin` | `{org}:org-admin` | `{org}-admin` (exists) |
| `user` | `{org}:user` | `{org}-user` (new) |

#### Project Namespace

| RoleBinding | Subject | Role | Created By |
|-------------|---------|------|------------|
| `org-admin` | `{org}:org-admin` | `admin` | Project controller (exists) |
| `user` | `{org}:user` | `user` | Project controller (new) |

**Note:** `developer` and `project-manager` RoleBindings are created by OrganizationGroup controller when admin assigns users to specific projects.

## Implementation Details

### Files Modified

1. **`internal/organization/helpers.go`** ✅ DONE
   - Added `DefaultKeycloakUserGroup` and `DefaultKeycloakUserRole` constants
   - Added `generateUserRoleName()` helper function

2. **`internal/organization/client_keycloak.go`** ✅ DONE
   - Added `user` group creation in `Create()` method

3. **`internal/organization/res_kube_role.go`** ✅ DONE
   - Added `NewRealmUserRole()` function for `{org}-user` role

4. **`internal/organization/res_kube_role_binding.go`** ✅ DONE
   - Added `NewRealmUserRoleBinding()` function

5. **`internal/organization/organization.go`** ✅ DONE
   - Added calls to sync user role and rolebinding in `Sync()`

6. **`internal/project/helpers.go`** ✅ DONE
   - Added role template constants and loader functions

7. **`internal/project/res_role.go`** ✅ DONE
   - Added `NewProjectDeveloperRole()`, `NewProjectManagerRole()`, `NewProjectUserRole()`

8. **`internal/project/res_role_binding.go`** ✅ DONE
   - Added `NewProjectUserRoleBinding()` for automatic `user` group binding

9. **`internal/project/project.go`** ✅ DONE
   - Added Sync and Delete calls for new roles and role bindings

10. **`api/kube-dc.com/v1/types.go`** ✅ DONE
    - Added role template name constants

11. **`charts/kube-dc/templates/default-project-admin-role.yaml`** ✅ DONE
    - Contains all 4 role templates: `admin`, `developer`, `project-manager`, `user`

### Controller Flow

```
Organization Created
    ├── Create Keycloak Realm
    │   └── Create Groups: org-admin, user
    ├── Create Org Namespace
    │   ├── Create Roles: {org}-admin, {org}-user
    │   └── Create RoleBindings: org-admin → {org}-admin, user → {org}-user
    └── Done

Project Created
    ├── Create Project Namespace
    │   ├── Create Roles from templates: admin, developer, project-manager, user
    │   └── Create RoleBindings: 
    │       ├── {org}:org-admin → admin (exists)
    │       └── {org}:user → user (new)
    └── Done

OrganizationGroup Created (for elevated access)
    ├── Create Keycloak Group (e.g., "my-developers")
    ├── For each project in spec.permissions:
    │   └── Create RoleBinding: {org}:my-developers → developer
    └── Done
```

## Role Permissions Matrix

### Organization Namespace

| Resource | org-admin | user |
|----------|-----------|------|
| organizations | get, list, patch, update, watch | get |
| projects | full CRUD | get, list |
| organizationgroups | full CRUD | - |

### Project Namespace

| Resource | admin | developer | project-manager | user |
|----------|-------|-----------|-----------------|------|
| virtualmachines | full CRUD | full CRUD | get, list, watch | get, list |
| virtualmachineinstances | full CRUD | full CRUD | get, list, watch | get, list |
| pods | full CRUD | full CRUD | get, list, watch | get, list |
| pods/log | get | get | get | get |
| services | full CRUD | full CRUD | get, list, watch | get, list |
| deployments | full CRUD | full CRUD | get, list, watch | get, list |
| secrets | full CRUD | full CRUD | get, list | - |
| configmaps | full CRUD | full CRUD | get, list | get, list |

## User Approval Flow

```
User requests to join organization
    ↓
Org admin sees join request in UI
    ↓
Admin selects group: "user" (default) or "org-admin"
    ↓
Backend adds user to Keycloak group
    ↓
User gets automatic read-only access to all projects
    ↓
(Optional) Admin creates OrganizationGroup for elevated per-project access
```

## Success Criteria

1. New organizations have `org-admin` and `user` groups in Keycloak
2. Join request approval adds users to `user` group by default
3. Users in `user` group have read-only access to all projects
4. Project creation includes `developer`, `project-manager`, `user` role templates
5. OrganizationGroup CRD can grant elevated access per-project

## Out of Scope

- Migration of existing organizations (recreate after approval)
- Custom group creation via UI (future feature)
- Per-project group overrides outside OrganizationGroup CRD
