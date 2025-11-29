# Signup Feature Implementation

## Overview
Implemented a complete self-service signup flow that allows users to create an account, organization, and project without admin approval or email verification.

## Backend Implementation

### 1. Public Signup Controller (`ui/backend/controllers/publicSignup.js`)
- **Endpoint**: `POST /api/public/signup`
- **Request Body**:
  ```json
  {
    "name": "Jane Doe",
    "email": "jane@example.com",
    "organizationName": "Acme Corp",
    "projectName": "Core Platform"
  }
  ```

#### Features:
- **Input Validation**: Validates all required fields, email format, and length constraints
- **K8s Name Normalization**: Converts display names to Kubernetes-safe identifiers (lowercase, alphanumeric + hyphens)
- **Unique Organization IDs**: Auto-generates unique org IDs by appending suffixes if conflicts exist
- **Automatic Resource Creation**:
  1. Organization namespace with labels and annotations
  2. Project CR in organization namespace
  3. Kubernetes Role (`resource-manager`) in project namespace
  4. OrganizationGroup CR (`{project}-users`) linking project to roles
  5. OrganizationGroup CR (`org-admin`) for organization administrator access
  6. **User account in Keycloak** with org-admin group assignment
  7. **Temporary password** (requires password reset on first login)
- **Error Handling**: Comprehensive error handling with cleanup on failure
- **Response**: Returns organization/project IDs and login URL with realm parameter

### 2. App Registration (`ui/backend/app.js`)
- Registered public signup router at `/api/public` path
- No authentication middleware (public endpoint)

### 3. Keycloak Integration
- **Admin Token**: Obtains Keycloak admin token using `KEYCLOAK_ADMIN_USER` and `KEYCLOAK_ADMIN_PASSWORD` environment variables
- **User Creation**: Creates user in the organization's Keycloak realm
- **Group Assignment**: Automatically assigns user to `org-admin` group
- **Password Management**: Sets temporary password that must be changed on first login
- **Email**: User can use "Forgot Password" flow to set their own password

## Frontend Implementation

### 1. Signup Page Component (`ui/frontend/src/app/Signup/SignupPage.tsx`)
- **Route**: `/signup`
- **Features**:
  - Clean form with 4 required fields: Name, Email, Organization Name, Project Name
  - Client-side validation with user-friendly error messages
  - Loading states during submission
  - Success screen with auto-redirect to login
  - "Already have an account? Log in" link

#### User Flow:
1. User fills out signup form
2. Client validates input
3. POST request to `/api/public/signup`
4. On success: Shows success message, redirects to login with realm parameter after 3 seconds
5. On error: Displays error message, allows retry

### 2. Route Registration (`ui/frontend/src/app/routes.tsx`)
- Added `/signup` route at the top of routes array (public route)
- No authentication required for this route

## Security Considerations

### Current Implementation:
- **Rate Limiting**: Not yet implemented (recommended for production)
- **CAPTCHA**: Not yet implemented (recommended to prevent bots)
- **Email Verification**: Not yet implemented (user must use "Forgot Password" to set password)
- **Admin Approval**: Intentionally skipped per requirements
- **Password Security**: Temporary random password generated, must be changed on first login
- **Keycloak Admin Credentials**: Stored in environment variables (`KEYCLOAK_ADMIN_PASSWORD`)

### Recommended Improvements for Production:
1. **Add Rate Limiting**: Limit signup attempts per IP/email
2. **Add CAPTCHA**: Integrate hCaptcha or reCAPTCHA
3. **Block Disposable Emails**: Filter known disposable email domains
4. **Audit Logging**: Log all signup attempts with IP addresses
5. **Resource Quotas**: Set default quotas for new organizations
6. **Email Verification** (optional): Add verification step before full access

## Default Project Configuration

New projects are created with:
- **CIDR**: `10.0.0.0/24`
- **Egress Network Type**: `public`
- **Gateway LAN IP**: `10.0.0.1`
- **Default Role**: `resource-manager` with permissions for pods, services, deployments, daemonsets, replicasets, statefulsets, jobs

## Organization Administrator Role

The signup process automatically creates an `org-admin` OrganizationGroup:
- **Purpose**: Grants organization-level administrative privileges
- **Permissions**: Full access to all projects within the organization
- **Assignment**: When users register in Keycloak for this organization, they will automatically be assigned to the `org-admin` group via the OrganizationGroup controller
- **Capabilities**: Can create/delete projects, manage users, view all resources

## User Experience Flow

1. User visits `/signup`
2. Fills in:
   - Full Name
   - Email
   - Organization Name (e.g., "Acme Corporation")
   - Project Name (e.g., "Production")
3. Clicks "Sign Up"
4. Backend creates:
   - Organization namespace: `acme-corporation` (normalized)
   - Project: `production` (normalized)
   - RBAC resources (resource-manager role, project users group, org-admin group)
   - **User account in Keycloak** with org-admin privileges
5. Success message displayed
6. Auto-redirect to login page with organization realm: `/?realm=acme-corporation`
7. User clicks "Forgot Password" to set their password (or checks email if email sending is configured)
8. User logs in via Keycloak with their chosen password

## Integration with Existing Systems

- **Keycloak**: Uses existing OIDC authentication flow
- **Kubernetes**: Leverages existing CRD patterns (Organization, Project, OrganizationGroup)
- **RBAC**: Follows existing role/group patterns from manage-organization module
- **UI**: Consistent with existing PatternFly components and styling

## Testing

### Manual Testing Steps:
1. Navigate to `/signup`
2. Fill out form with valid data
3. Submit and verify:
   - Organization namespace created
   - Project CR created in org namespace
   - Role created in project namespace
   - OrganizationGroup created
4. Verify redirect to login
5. Test error cases:
   - Missing fields
   - Invalid email
   - Short names
   - Duplicate organization names

### API Testing:
```bash
curl -X POST http://localhost:3333/api/public/signup \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Test User",
    "email": "test@example.com",
    "organizationName": "Test Org",
    "projectName": "Test Project"
  }'
```

## Future Enhancements

1. **Email Notifications**: Send welcome email with login instructions
2. **Onboarding Flow**: Guide new users through first steps
3. **Plan Selection**: Allow users to choose resource quotas/plans
4. **Team Invites**: Allow org owner to invite team members
5. **SSO Integration**: Support signup via Google, GitHub, etc.
6. **Multi-Project Creation**: Allow creating multiple projects during signup
7. **Custom Branding**: Organization-specific branding/themes

## Files Modified/Created

### Backend:
- ✅ Created: `ui/backend/controllers/publicSignup.js`
- ✅ Modified: `ui/backend/app.js`

### Frontend:
- ✅ Created: `ui/frontend/src/app/Signup/SignupPage.tsx`
- ✅ Modified: `ui/frontend/src/app/routes.tsx`

## Environment Variables Required

- `KEYCLOAK_URL`: Keycloak server URL (default: `https://login.dev.kube-dc.com`)
- `KEYCLOAK_ADMIN_USER`: Keycloak admin username (default: `admin`)
- `KEYCLOAK_ADMIN_PASSWORD`: **Required** - Keycloak admin password for API access

## Known Limitations

1. **No Email Sending**: Temporary password is set but not emailed to user - they must use "Forgot Password"
2. **Fixed Project Configuration**: All new projects use same default network settings
3. **No Rollback on Partial Failure**: If user creation fails, org/project remain (user can still be created manually)
4. **2-Second Sync Delay**: Waits 2 seconds for Keycloak group sync after OrganizationGroup creation
5. **Admin Credentials in Environment**: Keycloak admin password must be securely stored

## Next Steps

To enhance the signup flow, consider:
1. **Email Integration**: Send welcome email with temporary password or password reset link
2. **Email Verification**: Add email verification step before account activation
3. **Rate Limiting & CAPTCHA**: Implement to prevent abuse
4. **Audit Logging**: Comprehensive logging of signup attempts
5. **Admin Dashboard**: Monitor signups and user creation
6. **Password Policy**: Configure Keycloak password requirements
7. **Secure Credential Storage**: Use Kubernetes secrets for Keycloak admin credentials
