package k8sapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// ServedResources lists the resources the API server serves for one API
// group version (e.g. "security.kube-dc.com/v1alpha1"), from Kubernetes
// discovery. A group version the server does not serve answers nil, nil:
// the caller falls back to an older API.
func (c *Client) ServedResources(ctx context.Context, groupVersion string) ([]string, error) {
	path := "/apis/" + groupVersion
	if !strings.Contains(groupVersion, "/") {
		path = "/api/" + groupVersion // the core group: "v1"
	}
	var list struct {
		Resources []struct {
			Name string `json:"name"`
		} `json:"resources"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &list, ""); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(list.Resources))
	for _, r := range list.Resources {
		out = append(out, r.Name)
	}
	return out, nil
}
