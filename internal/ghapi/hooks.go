package ghapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// WebhookPath is the base URL path arc's webhook endpoint serves. Each host
// appends its host id (WebhookPath + "/" + hostid), and that full path is the
// marker identifying which of an account's webhooks belong to which arc host
// — so re-registration updates this host's hook without stealing another's.
const WebhookPath = "/arc/webhook"

// AccountType reports whether login is a "User" or an "Organization".
func (c *Client) AccountType(ctx context.Context, login string) (string, error) {
	var out struct {
		Type string `json:"type"`
	}
	if _, err := c.getJSON(ctx, "/users/"+login, "", &out); err != nil {
		return "", fmt.Errorf("look up account %s: %w", login, err)
	}
	return out.Type, nil
}

type hookInfo struct {
	ID     int64 `json:"id"`
	Config struct {
		URL string `json:"url"`
	} `json:"config"`
}

// EnsureWebhook points this host's arc webhook for repo (or the org, when
// repo is empty) at url, creating or updating as needed. Ours is the hook
// whose URL ends in url's path — tunnel hostnames change every start, the
// path never does. The secret rotates on every arc start, so an existing
// hook is always rewritten.
func (c *Client) EnsureWebhook(ctx context.Context, repo, url, secret string) error {
	marker := hookMarker(url)
	base := c.hooksBase(repo)

	existing, err := c.listHooks(ctx, base)
	if err != nil {
		return err
	}

	body := map[string]any{
		"active": true,
		"events": []string{"workflow_job"},
		"config": map[string]any{
			"url":          url,
			"content_type": "json",
			"secret":       secret,
		},
	}

	for _, h := range existing {
		if !strings.HasSuffix(h.Config.URL, marker) {
			continue
		}
		path := fmt.Sprintf("%s/%d", base, h.ID)
		if _, _, err := c.request(ctx, http.MethodPatch, path, body, ""); err != nil {
			return fmt.Errorf("update hook %d: %w", h.ID, err)
		}
		return nil
	}

	body["name"] = "web"
	if _, _, err := c.request(ctx, http.MethodPost, base, body, ""); err != nil {
		return fmt.Errorf("create hook: %w", err)
	}
	return nil
}

// DeleteWebhook removes this host's arc webhook from repo (or the org, when
// repo is empty) and reports how many it deleted. url identifies the hook the
// same way EnsureWebhook does — by its path, which carries the host id — so
// uninstalling one host never unhooks another. Zero deleted is the normal
// answer for an account that never had webhooks registered.
func (c *Client) DeleteWebhook(ctx context.Context, repo, url string) (int, error) {
	marker := hookMarker(url)
	base := c.hooksBase(repo)

	existing, err := c.listHooks(ctx, base)
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, h := range existing {
		if !strings.HasSuffix(h.Config.URL, marker) {
			continue
		}
		path := fmt.Sprintf("%s/%d", base, h.ID)
		if _, _, err := c.request(ctx, http.MethodDelete, path, nil, ""); err != nil {
			if IsNotFound(err) {
				continue // already gone; the desired state holds
			}
			return deleted, fmt.Errorf("delete hook %d: %w", h.ID, err)
		}
		deleted++
	}
	return deleted, nil
}

// hookMarker reduces a webhook URL to the part that identifies an arc host.
// Tunnel hostnames change on every start; the path never does.
func hookMarker(url string) string {
	if i := strings.Index(url, WebhookPath); i >= 0 {
		return url[i:]
	}
	return url
}

// hooksBase is the API path for a repo's hooks, or the org's when repo is empty.
func (c *Client) hooksBase(repo string) string {
	if repo != "" {
		return fmt.Sprintf("/repos/%s/%s/hooks", c.entity(), repo)
	}
	return fmt.Sprintf("/orgs/%s/hooks", c.org)
}

func (c *Client) listHooks(ctx context.Context, base string) ([]hookInfo, error) {
	var all []hookInfo
	err := c.paginate(ctx, base+"?per_page=100", "", func(page []byte) error {
		var hooks []hookInfo
		if err := json.Unmarshal(page, &hooks); err != nil {
			return fmt.Errorf("decode hooks: %w", err)
		}
		all = append(all, hooks...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list hooks: %w", err)
	}
	return all, nil
}
