// Package client is the HTTP client that talks to the Office Town
// worker's /api/sync/* endpoints.
//
// Replaces the AWS SDK / direct R2 access from earlier versions. We
// don't need R2 credentials anymore — the worker proxies all R2 ops
// via its bindings. We just need a worker URL + a bearer token.
//
// Why this is better:
//   - Zero R2 credential setup for the user
//   - All writes audited server-side (worker logs every PUT/DELETE)
//   - Markdown frontmatter repaired on the way through (worker calls
//     Workers AI if YAML doesn't parse)
//   - Multi-machine writes serialise through the worker
//   - Smaller dependency tree (no aws-sdk-go-v2)
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Client talks to the worker over HTTP using the MCP bearer.
type Client struct {
	BaseURL   string // e.g. https://office-town.jezweb.workers.dev
	Bearer    string
	MachineID string // forwarded as X-Office-Town-Machine for audit log
	DeviceID  string // forwarded as X-Office-Town-Device — stable machine identity
	http      *http.Client
}

// New constructs a Client. baseURL must NOT have a trailing slash.
func New(baseURL, bearer, machineID, deviceID string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	if machineID == "" {
		// Best-effort machine fingerprint — hostname is fine for the
		// audit log; we're not relying on it for auth.
		host, _ := os.Hostname()
		machineID = host
		if machineID == "" {
			machineID = "unknown"
		}
	}
	return &Client{
		BaseURL:   baseURL,
		Bearer:    bearer,
		MachineID: machineID,
		DeviceID:  deviceID,
		http:      &http.Client{Timeout: 60 * time.Second},
	}
}

// Object is the slim view we get back from the worker's list endpoint.
type Object struct {
	Key          string    `json:"key"`
	ETag         string    `json:"etag"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// PutResult is what the worker returns from PUT /api/sync/object.
type PutResult struct {
	Key        string `json:"key"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
	Hash       string `json:"hash"`
	Repaired   bool   `json:"repaired"`
	RepairNote string `json:"repair_note,omitempty"`
	AuditID    string `json:"audit_id"`
}

// ErrNotFound is returned by Head/Get when the key doesn't exist.
var ErrNotFound = errors.New("client: object not found")

// authedRequest builds a request with the bearer + machine-id headers.
func (c *Client) authedRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Bearer)
	if c.MachineID != "" {
		req.Header.Set("X-Office-Town-Machine", c.MachineID)
	}
	if c.DeviceID != "" {
		req.Header.Set("X-Office-Town-Device", c.DeviceID)
	}
	return req, nil
}

// List returns every object under the given prefix. Walks pagination
// internally.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	cursor := ""
	for {
		q := url.Values{}
		q.Set("prefix", prefix)
		q.Set("limit", "1000")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, err := c.authedRequest(ctx, http.MethodGet, "/api/sync/list?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list http: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			return nil, fmt.Errorf("list %s: %d %s", prefix, resp.StatusCode, body)
		}
		var page struct {
			Objects   []Object `json:"objects"`
			Truncated bool     `json:"truncated"`
			Cursor    string   `json:"cursor"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list decode: %w", err)
		}
		out = append(out, page.Objects...)
		if !page.Truncated {
			break
		}
		cursor = page.Cursor
		if cursor == "" {
			break
		}
	}
	return out, nil
}

// Head returns the object's metadata, or ErrNotFound if missing.
func (c *Client) Head(ctx context.Context, key string) (*Object, error) {
	req, err := c.authedRequest(ctx, http.MethodHead, "/api/sync/object/"+key, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("head http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("head %s: %d", key, resp.StatusCode)
	}
	lm, _ := time.Parse(time.RFC1123, resp.Header.Get("Last-Modified"))
	size := int64(0)
	fmt.Sscanf(resp.Header.Get("Content-Length"), "%d", &size)
	return &Object{
		Key:          key,
		ETag:         strings.Trim(resp.Header.Get("ETag"), `"`),
		Size:         size,
		LastModified: lm,
	}, nil
}

// Get downloads an object. Returns body bytes + metadata.
func (c *Client) Get(ctx context.Context, key string) ([]byte, *Object, error) {
	req, err := c.authedRequest(ctx, http.MethodGet, "/api/sync/object/"+key, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("get http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("get %s: %d %s", key, resp.StatusCode, body)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read body for %s: %w", key, err)
	}
	lm, _ := time.Parse(time.RFC1123, resp.Header.Get("Last-Modified"))
	return body, &Object{
		Key:          key,
		ETag:         strings.Trim(resp.Header.Get("ETag"), `"`),
		Size:         int64(len(body)),
		LastModified: lm,
	}, nil
}

// Put uploads content with content-type derived from the filename extension.
// reason is included as the X-Office-Town-Why header for audit purposes.
func (c *Client) Put(ctx context.Context, key string, body []byte, reason string) (*PutResult, error) {
	req, err := c.authedRequest(ctx, http.MethodPut, "/api/sync/object/"+key, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if ctype := mime.TypeByExtension(filepath.Ext(key)); ctype != "" {
		req.Header.Set("Content-Type", ctype)
	} else {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	if reason != "" {
		req.Header.Set("X-Office-Town-Why", reason)
	}
	req.ContentLength = int64(len(body))
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("put http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("put %s: %d %s", key, resp.StatusCode, rb)
	}
	var pr PutResult
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("put decode: %w", err)
	}
	return &pr, nil
}

// Delete removes an object. Not an error if it was already gone.
func (c *Client) Delete(ctx context.Context, key, reason string) error {
	req, err := c.authedRequest(ctx, http.MethodDelete, "/api/sync/object/"+key, nil)
	if err != nil {
		return err
	}
	if reason != "" {
		req.Header.Set("X-Office-Town-Why", reason)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	rb, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("delete %s: %d %s", key, resp.StatusCode, rb)
}

// Heartbeat reports a sync pass to the worker so the cortex knows the daemon is
// alive and when it last synced. Fire-and-forget: callers ignore the error (a
// missed heartbeat must never affect syncing).
func (c *Client) Heartbeat(ctx context.Context, version string, stats any) error {
	payload, err := json.Marshal(map[string]any{
		"machine":  c.MachineID,
		"version":  version,
		"platform": runtime.GOOS + "/" + runtime.GOARCH,
		"stats":    stats,
	})
	if err != nil {
		return err
	}
	req, err := c.authedRequest(ctx, http.MethodPost, "/api/sync/heartbeat", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("heartbeat: %d", resp.StatusCode)
	}
	return nil
}

// Credentials describes what `officetowd configure --from-dashboard` fetches.
type Credentials struct {
	WorkerURL  string `json:"worker_url"`
	BearerHint string `json:"bearer_hint"`
}

// FetchCredentials hits the worker's /api/sync/credentials endpoint.
// Used by `officetowd configure --from-dashboard` to sanity-check the
// worker URL + grab any guidance the worker wants to surface. The
// bearer is NOT returned by the endpoint — the user/agent supplies it
// separately.
func (c *Client) FetchCredentials(ctx context.Context) (*Credentials, error) {
	req, err := c.authedRequest(ctx, http.MethodGet, "/api/sync/credentials", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("credentials http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("credentials: %d %s", resp.StatusCode, rb)
	}
	var creds Credentials
	if err := json.NewDecoder(resp.Body).Decode(&creds); err != nil {
		return nil, fmt.Errorf("credentials decode: %w", err)
	}
	return &creds, nil
}
