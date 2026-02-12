# PRD: OIDC Dynamic Reload - Issues and Solutions

## Overview

This document describes the issues encountered during the implementation of OIDC dynamic reload for new organization creation in Kube-DC, along with proposed solutions.

## Background

When a new organization is created in Kube-DC:
1. A new Keycloak realm is created for the organization
2. The OIDC configuration is updated in `auth-conf.yaml` ConfigMap
3. The DaemonSet syncs the config to all control plane nodes
4. The Kubernetes API server needs to reload the OIDC configuration
5. Only then can tokens from the new realm be accepted

**Problem**: There is a timing window between org creation and OIDC config reload where tokens are rejected with 401 Unauthorized.

## Issues Identified

### Issue 1: CORS Error on `/api/register/verify-token`

**Symptoms**:
```
Access to fetch at 'https://backend.kube-dc.cloud/api/register/verify-token' 
from origin 'https://console.kube-dc.cloud' has been blocked by CORS policy: 
No 'Access-Control-Allow-Origin' header is present on the requested resource.
```

**Root Cause**:
The 504 Gateway Timeout response from the ingress does not include CORS headers. When the backend times out, the ingress returns a 504 without forwarding CORS headers from the backend.

**Solution Options**:
1. Add CORS headers at the ingress level for error responses
2. Reduce the verify-token timeout to stay within ingress timeout limits
3. Move the polling logic to the frontend (client-side polling)

---

### Issue 2: 504 Gateway Timeout on `/api/register/verify-token`

**Symptoms**:
```
POST https://backend.kube-dc.cloud/api/register/verify-token
net::ERR_FAILED 504 (Gateway Timeout)
```

**Root Cause**:
The verify-token endpoint polls K8s API for up to 30 seconds (15 attempts × 2s delay). This exceeds the default ingress timeout (typically 60s, but can be lower).

**Current Implementation**:
```javascript
const maxAttempts = 15; // 15 attempts
const delayMs = 2000;   // 2 seconds between attempts
// Total time: up to 30 seconds
```

**Solution Options**:
1. **Reduce polling time**: Lower maxAttempts or delayMs to stay within timeout
2. **Async polling**: Return immediately and let frontend poll for status
3. **Increase ingress timeout**: Configure higher timeout for this specific endpoint
4. **WebSocket**: Use WebSocket for real-time status updates

---

### Issue 3: Login Screen Still Appears After "Go to Dashboard"

**Symptoms**:
User clicks "Go to Dashboard" after org creation but is redirected to login screen instead of entering dashboard directly.

**Root Cause Analysis**:

The current flow in `AppLayout.tsx`:
```typescript
const handleGoToDashboard = () => {
  const orgToken = localStorage.getItem('org-token');
  if (orgToken) {
    localStorage.setItem('token', orgToken);
  }
  localStorage.setItem('organization', createdOrgName);
  window.location.href = `${window.location.origin}/manage-organization/projects?realm=${createdOrgName}`;
};
```

**Problems**:
1. The `token` is set in localStorage but the app's auth context may not recognize it
2. The app may use `oidc-client-ts` which checks for session state, not just localStorage token
3. Navigation to `/manage-organization/projects` triggers route guards that check auth state
4. The auth state check may redirect to login because OIDC session is not established

**Solution Options**:
1. **Set auth context directly**: Update the OIDC auth context with the new token
2. **Use programmatic login**: Trigger OIDC login flow with the org realm
3. **Token injection**: Properly inject token into auth provider's state
4. **Session storage**: Also set token in session storage if auth library uses it

---

### Issue 4: OIDC Config Reload Timing

**Symptoms**:
Even with controller-side waiting (10s), the API server may not have reloaded by the time the user clicks "Go to Dashboard".

**Root Cause**:
1. Controller waits 10s for OIDC reload (may not be enough)
2. DaemonSet sync has its own timing (checks every few seconds)
3. API server file watcher has debounce/delay
4. Multiple control plane nodes need to reload

**Current Flow**:
```
1. Org created in controller
2. Auth config updated in ConfigMap
3. Controller waits 10s for OIDC reload
4. DaemonSet syncs config to nodes (every 5s check)
5. API server detects file change
6. API server reloads OIDC config
7. Token becomes valid
```

**Timing Breakdown**:
- ConfigMap update: immediate
- DaemonSet poll interval: 5s
- File write to node: < 1s
- API server file watcher: 1-5s debounce
- OIDC config reload: 1-2s
- **Total worst case**: ~15s

---

## Proposed Solutions

### Solution A: Frontend-Side Polling (Recommended)

Instead of backend polling, move the verification to frontend:

**Frontend Changes**:
```typescript
// After org creation, show "Preparing your workspace..." with spinner
// Poll a lightweight endpoint that checks token validity
const verifyOrgReady = async (token: string, org: string): Promise<boolean> => {
  try {
    const response = await fetch(`${backendURL}/api/manage-organization/projects`, {
      headers: { 'Authorization': `Bearer ${token}` }
    });
    return response.status !== 401;
  } catch {
    return false;
  }
};

// Poll until ready or timeout
const pollUntilReady = async (token: string, org: string, maxAttempts = 15): Promise<boolean> => {
  for (let i = 0; i < maxAttempts; i++) {
    if (await verifyOrgReady(token, org)) return true;
    await sleep(2000);
  }
  return false;
};
```

**Benefits**:
- No ingress timeout issues
- User sees progress
- Can be cancelled by user
- No CORS issues (same-origin)

---

### Solution B: Increase Controller Wait Time

Increase the OIDC reload wait time in the controller to ensure config is loaded before returning.

**Changes**:
```go
// organization.go
if err := kubeAuthCli.WaitForOIDCConfigReload(ctx, 30*time.Second); err != nil {
    log.V(5).Info("OIDC reload verification failed", "error", err)
}
```

**Risks**:
- Backend registration timeout (currently 30s)
- User waits longer during org creation
- May still not be enough on slow clusters

---

### Solution C: Fix Authentication Flow

Properly integrate org-token with OIDC auth library:

**Changes**:
```typescript
// AppLayout.tsx
const handleGoToDashboard = async () => {
  const orgToken = localStorage.getItem('org-token');
  if (orgToken) {
    // Decode token to get expiration and user info
    const decoded = decodeJWT(orgToken);
    
    // Create a proper User object for oidc-client-ts
    const user = new User({
      access_token: orgToken,
      token_type: 'Bearer',
      profile: decoded,
      expires_at: decoded.exp,
    });
    
    // Store in auth manager
    await auth.storeUser(user);
    
    // Set localStorage for API calls
    localStorage.setItem('token', orgToken);
  }
  
  localStorage.setItem('organization', createdOrgName);
  
  // Use React Router navigation instead of full page reload
  history.push(`/manage-organization/projects`);
};
```

---

## Recommended Implementation Plan

### Phase 1: Quick Fix (Immediate)

1. **Remove backend verify-token endpoint** (causes timeout/CORS issues)
2. **Increase controller OIDC wait** to 20s
3. **Add frontend polling** after "Go to Dashboard" click:
   - Show "Connecting to your workspace..." spinner
   - Retry API calls on 401 (already implemented)
   - Max 30s total wait

### Phase 2: Proper Fix (Short-term)

1. **Fix authentication flow**:
   - Store org-token properly in auth context
   - Use SPA history navigation, not full page reload
   - Ensure auth guards recognize the new token

2. **Add visual feedback**:
   - Progress indicator during org creation
   - Clear error messages if timeout occurs
   - "Click to retry" option on failure

### Phase 3: Robust Solution (Long-term)

1. **WebSocket status updates**:
   - Real-time OIDC reload status from controller
   - Frontend subscribes to org creation events
   - No polling needed

2. **API server readiness check**:
   - Controller queries API server metrics
   - Confirms new issuer is in loaded config
   - Only then marks org as ready

---

## Testing Checklist

- [ ] Create new org → enters dashboard without login prompt
- [ ] Create new org → no 401 errors in console
- [ ] Create new org → projects page loads immediately
- [ ] Timeout scenario → graceful error message
- [ ] Refresh after org creation → still logged in
- [ ] Multiple orgs → can switch between them

---

## Files Affected

### Backend
- `ui/backend/controllers/registration/index.js` - Remove or fix verify-token endpoint

### Frontend
- `ui/frontend/src/app/Registration/SetupOrgPage.tsx` - Add polling logic
- `ui/frontend/src/app/AppLayout/AppLayout.tsx` - Fix handleGoToDashboard
- `ui/frontend/src/app/utils/fetchWithRetry.ts` - Already implemented

### Controller
- `internal/organization/organization.go` - Adjust OIDC wait timeout
- `internal/organization/client_kube_auth.go` - WaitForOIDCConfigReload function

---

## References

- Kubernetes OIDC Authentication: https://kubernetes.io/docs/reference/access-authn-authz/authentication/#openid-connect-tokens
- K8s 1.30 Structured Authentication Config: https://kubernetes.io/docs/reference/access-authn-authz/authentication/#using-authentication-configuration
- oidc-client-ts: https://github.com/authts/oidc-client-ts
