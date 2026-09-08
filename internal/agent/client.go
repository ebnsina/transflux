package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ebnsina/transflux/internal/artifact"
	"github.com/ebnsina/transflux/internal/job"
	"github.com/ebnsina/transflux/internal/worker"
	"github.com/google/uuid"
)

// ErrNoWork mirrors the control plane's 204: an idle fleet, not a failure.
var ErrNoWork = errors.New("no work available")

// Client speaks the worker protocol. Every call is outbound: the control plane
// never dials a worker, which is what lets one run behind NAT (ADR-006).
type Client struct {
	baseURL    string
	http       *http.Client
	credential string
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		// Generous, because a lease poll long-polls and an upload of logs can
		// be slow. Individual calls override it where a bound matters.
		http: &http.Client{Timeout: 2 * time.Minute},
	}
}

func (c *Client) Register(ctx context.Context, bootstrapToken string, reg worker.Registration) (uuid.UUID, time.Duration, error) {
	var out struct {
		Worker            worker.Worker `json:"worker"`
		Credential        string        `json:"credential"`
		HeartbeatInterval int           `json:"heartbeat_interval_seconds"`
	}
	if err := c.do(ctx, http.MethodPost, "/worker/v1/register", bootstrapToken, reg, &out); err != nil {
		return uuid.Nil, 0, err
	}
	// From here on the shared bootstrap token is never used again.
	c.credential = out.Credential
	return out.Worker.ID, time.Duration(out.HeartbeatInterval) * time.Second, nil
}

func (c *Client) Heartbeat(ctx context.Context, rep worker.Report) (string, error) {
	var out struct {
		State string `json:"state"`
	}
	err := c.do(ctx, http.MethodPost, "/worker/v1/heartbeat", c.credential, rep, &out)
	return out.State, err
}

// Lease asks for work. ErrNoWork is the normal answer for an idle fleet.
func (c *Client) Lease(ctx context.Context) (job.Assignment, error) {
	var a job.Assignment
	err := c.do(ctx, http.MethodPost, "/worker/v1/lease", c.credential, nil, &a)
	return a, err
}

func (c *Client) Started(ctx context.Context, attemptID uuid.UUID) error {
	return c.do(ctx, http.MethodPost, "/worker/v1/attempts/"+attemptID.String()+"/started",
		c.credential, nil, nil)
}

// Progress renews the lease and returns whether the task has been cancelled.
// This is the only channel by which a cancellation reaches us.
func (c *Client) Progress(ctx context.Context, attemptID uuid.UUID, pct float32) (bool, error) {
	var out struct {
		Cancel bool `json:"cancel"`
	}
	err := c.do(ctx, http.MethodPost, "/worker/v1/attempts/"+attemptID.String()+"/progress",
		c.credential, map[string]float32{"progress_pct": pct}, &out)
	return out.Cancel, err
}

// UploadURL asks where one output file should go. The worker names a path
// relative to its own task and never chooses an absolute location.
func (c *Client) UploadURL(ctx context.Context, attemptID uuid.UUID, path string) (url, key string, err error) {
	var out struct {
		URL        string `json:"url"`
		StorageKey string `json:"storage_key"`
	}
	err = c.do(ctx, http.MethodPost, "/worker/v1/attempts/"+attemptID.String()+"/upload-url",
		c.credential, map[string]string{"path": path}, &out)
	return out.URL, out.StorageKey, err
}

// RegisterArtifact records one output. Registering before completing means a
// set is never closed with an artifact missing from it.
func (c *Client) RegisterArtifact(ctx context.Context, attemptID uuid.UUID, reg artifact.Registration) error {
	return c.do(ctx, http.MethodPost, "/worker/v1/attempts/"+attemptID.String()+"/artifacts",
		c.credential, reg, nil)
}

func (c *Client) Complete(ctx context.Context, attemptID uuid.UUID, res job.Result) error {
	return c.do(ctx, http.MethodPost, "/worker/v1/attempts/"+attemptID.String()+"/complete",
		c.credential, res, nil)
}

func (c *Client) do(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNoContent:
		if out != nil {
			return ErrNoWork
		}
		return nil
	case resp.StatusCode == http.StatusConflict:
		// The lease is gone. Stop immediately rather than finishing work
		// nobody will accept.
		return job.ErrStaleAttempt
	case resp.StatusCode >= 300:
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(detail))
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
