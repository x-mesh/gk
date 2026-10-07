package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WorkflowRun is the GitHub Actions run data needed to select and watch a
// workflow. The API supplies more fields, which gk deliberately does not own.
type WorkflowRun struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	HeadSHA    string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	HeadBranch string    `json:"head_branch"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListWorkflowRuns returns the newest Actions runs for a commit. If workflow
// is set, it filters by GitHub's workflow display name.
func (c *Client) ListWorkflowRuns(ctx context.Context, owner, repo, sha, workflow string) ([]WorkflowRun, error) {
	q := url.Values{}
	q.Set("head_sha", sha)
	q.Set("per_page", "100")
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/runs?%s", url.PathEscape(owner), url.PathEscape(repo), q.Encode()))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("github actions runs returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode actions runs: %w", err)
	}
	if workflow == "" {
		return payload.WorkflowRuns, nil
	}
	runs := make([]WorkflowRun, 0, len(payload.WorkflowRuns))
	for _, run := range payload.WorkflowRuns {
		if run.Name == workflow {
			runs = append(runs, run)
		}
	}
	return runs, nil
}

// GetWorkflowRun returns the current state of one Actions run.
func (c *Client) GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (WorkflowRun, error) {
	resp, err := c.get(ctx, fmt.Sprintf("/repos/%s/%s/actions/runs/%d", url.PathEscape(owner), url.PathEscape(repo), runID))
	if err != nil {
		return WorkflowRun{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return WorkflowRun{}, fmt.Errorf("github actions run returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var run WorkflowRun
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		return WorkflowRun{}, fmt.Errorf("decode actions run: %w", err)
	}
	return run, nil
}

// ListWorkflowRunsForSHA is ListWorkflowRuns made conditional on etag, for
// a poller that re-asks about one pushed commit until its runs finish.
// notModified means GitHub answered 304 and runs is nil.
func (c *Client) ListWorkflowRunsForSHA(ctx context.Context, owner, repo, sha, etag string) (runs []WorkflowRun, newETag string, notModified bool, err error) {
	q := url.Values{}
	q.Set("head_sha", sha)
	q.Set("per_page", "100")
	return c.listRunsConditional(ctx, fmt.Sprintf("/repos/%s/%s/actions/runs?%s", url.PathEscape(owner), url.PathEscape(repo), q.Encode()), etag)
}

// ListRecentWorkflowRuns returns a repository's newest Actions runs on any
// commit, conditionally on etag. notModified means GitHub answered 304 and
// runs is nil; the caller keeps its previous view. One call covers every run
// of the repo, so a watcher polls this instead of each run separately.
func (c *Client) ListRecentWorkflowRuns(ctx context.Context, owner, repo, etag string) (runs []WorkflowRun, newETag string, notModified bool, err error) {
	return c.listRunsConditional(ctx, fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=20", url.PathEscape(owner), url.PathEscape(repo)), etag)
}

func (c *Client) listRunsConditional(ctx context.Context, path, etag string) (runs []WorkflowRun, newETag string, notModified bool, err error) {
	resp, err := c.getConditional(ctx, path, etag)
	if err != nil {
		return nil, etag, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, etag, true, nil
	}
	if rl := asRateLimit(resp, time.Now()); rl != nil {
		return nil, etag, false, rl
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, etag, false, fmt.Errorf("github actions runs returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, etag, false, fmt.Errorf("decode actions runs: %w", err)
	}
	return payload.WorkflowRuns, resp.Header.Get("ETag"), false, nil
}
