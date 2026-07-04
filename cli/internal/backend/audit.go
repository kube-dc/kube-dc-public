// Typed wrappers for the /api/audit/* query surface (M1-T05b + T14).
// Three reads + one streamed export are exposed:
//
//   GET /api/audit/orgs/:org/projects/:project   project-scoped events
//   GET /api/audit/orgs/:org                     org-wide events (org-admin)
//   GET /api/audit/orgs/:org/csv                 streamed CSV export (org-admin)
//
// The backend returns events as { ts: <epoch-ns string>, body: {...} }
// — `body` is the original JSON the audit emitter pushed to Loki
// (actor, actor_groups, resource, request_id, source_ip,
// elevation_id, extra). We preserve `body` as map[string]any so the
// CLI can render new fields the backend adds without a coordinated
// client release.

package backend

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// AuditEvent is one normalised audit event.
type AuditEvent struct {
	TS   string         `json:"ts"`   // nanosecond epoch as string
	Body map[string]any `json:"body"` // raw body (actor/action/result/...)
}

// AuditList is the JSON-mode response shape.
type AuditList struct {
	Events   []AuditEvent `json:"events"`
	Query    string       `json:"query"`    // the underlying LogQL — useful for debugging
	Returned int          `json:"returned"` // == len(Events); echoed for convenience
	Limit    int          `json:"limit"`    // the limit the backend actually applied
}

// AuditQuery groups the optional filters. Empty strings / zero values
// are omitted from the query string so the backend's defaults apply.
type AuditQuery struct {
	Service string // secrets|certificates|kms|db-credentials|org-admin|audit|...
	Actor   string // substring match against preferred_username/email/sub
	Result  string // allowed|denied|error
	Since   string // epoch seconds OR RFC3339
	Until   string // epoch seconds OR RFC3339
	Limit   int    // 1..MAX_LIMIT (backend caps; 0 = backend default)
}

// QueryString renders AuditQuery as a `?…` suffix. Empty/zero fields
// are dropped; surviving fields are URL-encoded. Exported so the
// kube-dc CLI tests can pin the wire shape (the helper is on a public
// type, so exporting it doesn't enlarge the SDK contract).
func (q AuditQuery) QueryString() string {
	v := url.Values{}
	if q.Service != "" {
		v.Set("service", q.Service)
	}
	if q.Actor != "" {
		v.Set("actor", q.Actor)
	}
	if q.Result != "" {
		v.Set("result", q.Result)
	}
	if q.Since != "" {
		v.Set("since", q.Since)
	}
	if q.Until != "" {
		v.Set("until", q.Until)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// ListProjectAudit fetches events scoped to (org, project). The
// caller must be a member of the project (or org-admin); the backend
// enforces this via the namespace annotation lookup.
func (c *Client) ListProjectAudit(ctx context.Context, org, project string, q AuditQuery) (*AuditList, error) {
	p := "/api/audit/orgs/" + pathEscape(org) + "/projects/" + pathEscape(project) + q.QueryString()
	var out AuditList
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListOrgAudit fetches org-wide events. Org-admin only at the backend.
func (c *Client) ListOrgAudit(ctx context.Context, org string, q AuditQuery) (*AuditList, error) {
	p := "/api/audit/orgs/" + pathEscape(org) + q.QueryString()
	var out AuditList
	if err := c.do(ctx, "GET", p, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExportOrgAuditCSV streams the org-wide CSV export. Returns the raw
// CSV bytes (header row + one row per event). Org-admin only.
// Backend uses queryRangeStream so the response is bounded by limit
// rather than holding everything in memory; the CLI just buffers the
// full body for `--csv` output to stdout or a file.
func (c *Client) ExportOrgAuditCSV(ctx context.Context, org string, q AuditQuery) ([]byte, error) {
	p := "/api/audit/orgs/" + pathEscape(org) + "/csv" + q.QueryString()
	return c.doRaw(ctx, "GET", p, nil)
}

// FormatEpochNs converts the backend's nanosecond-epoch string into a
// human-readable RFC3339Nano timestamp in UTC.
//
// The backend's auditModule already ships `ts` as an ISO 8601 string
// (loki.js:104), so this helper is the SECONDARY display path —
// invoked only if a future code path forwards raw ns. Returns the
// original input verbatim when it doesn't parse as int64, so an ISO
// 8601 string passes through unchanged and a malformed value lands
// in the audit table as-is rather than crashing the CLI.
func FormatEpochNs(epochNs string) string {
	if epochNs == "" {
		return ""
	}
	ns, err := strconv.ParseInt(epochNs, 10, 64)
	if err != nil {
		return epochNs
	}
	return time.Unix(0, ns).UTC().Format(time.RFC3339Nano)
}
