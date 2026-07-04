// Typed wrappers for the /api/orgs/:org/projects/:project/elevate
// surface (M1-T06 + T14). The backend gates these to org-admin; the
// CLI layer just provides the verb shape and JSON marshalling — it
// doesn't second-guess the gate.

package backend

import (
	"context"
	"fmt"
)

// Grant is the response shape from POST /elevate, GET /elevate (when
// active), and DELETE /elevate. Field names mirror the backend's
// shapeGrant() in ui/backend/controllers/elevationModule.js so the
// JSON contract is 1:1.
type Grant struct {
	ElevationID string `json:"elevationId"`
	User        string `json:"user"`
	Org         string `json:"org"`
	Project     string `json:"project"`
	Reason      string `json:"reason"`
	GrantedAt   string `json:"grantedAt"`
	ExpiresAt   string `json:"expiresAt"`
	TTLSeconds  int    `json:"ttlSeconds"`
	// ReleasedAt is set only on the DELETE response.
	ReleasedAt string `json:"releasedAt,omitempty"`
}

// Status is the response shape from GET /elevate. The backend returns
// `{ active: false }` when no grant exists for the actor; otherwise
// it returns `{ active: true, ...Grant }`. We model the union as a
// single struct with an Active flag because that's how callers want
// to switch on it.
type ElevationStatus struct {
	Active bool `json:"active"`
	Grant       // inline when Active == true; zero values when not.
}

type ElevationList struct {
	Items []Grant `json:"items"`
}

// Elevate creates (or replaces) the elevation for the caller in
// (org, project). Returns the grant including the elevation_id that
// will be stamped on subsequent audit events.
func (c *Client) Elevate(ctx context.Context, org, project, reason string) (*Grant, error) {
	if reason == "" {
		return nil, fmt.Errorf("elevate: --reason is required")
	}
	p := "/api/orgs/" + pathEscape(org) + "/projects/" + pathEscape(project) + "/elevate"
	var out Grant
	if err := c.do(ctx, "POST", p, map[string]any{"reason": reason}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReleaseElevation ends the elevation early.
func (c *Client) ReleaseElevation(ctx context.Context, org, project string) (*Grant, error) {
	p := "/api/orgs/" + pathEscape(org) + "/projects/" + pathEscape(project) + "/elevate"
	var out Grant
	if err := c.do(ctx, "DELETE", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetElevationStatus returns whether the caller currently has an
// active elevation for (org, project).
func (c *Client) GetElevationStatus(ctx context.Context, org, project string) (*ElevationStatus, error) {
	p := "/api/orgs/" + pathEscape(org) + "/projects/" + pathEscape(project) + "/elevate"
	var out ElevationStatus
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListElevations returns all active elevations in the org. Org-admin
// only; non-admin tokens 403 at the backend.
func (c *Client) ListElevations(ctx context.Context, org string) (*ElevationList, error) {
	p := "/api/orgs/" + pathEscape(org) + "/elevations"
	var out ElevationList
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
