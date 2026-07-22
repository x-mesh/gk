package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// NewPullRequest is the input for CreatePullRequest. Head names the source
// branch: a bare "branch" when it lives in the target repository, or
// "owner:branch" when it lives in a fork.
type NewPullRequest struct {
	Title string
	Body  string
	Head  string
	Base  string
	Draft bool
}

// PullRequest is the slice of GitHub's pull-request JSON gk reports back.
// URL is html_url — the page a human opens, not the API resource.
type PullRequest struct {
	Number int
	Title  string
	URL    string
	State  string
	Draft  bool
}

// ErrPRAlreadyExists reports the 422 GitHub returns when an open pull request
// already covers this head→base pair. It is separate from other validation
// failures because it is recoverable: the caller can look the existing PR up
// (FindOpenPR) and point at it instead of reporting a failure.
var ErrPRAlreadyExists = errors.New("a pull request for this branch already exists")

// ErrNoCommitsBetween reports the 422 for a head that is not ahead of base.
// Distinguished for the same reason: the fix (commit, or pick another base)
// is specific and worth naming.
var ErrNoCommitsBetween = errors.New("no commits between base and head")

// CreatePullRequest opens a pull request against owner/repo.
//
// This is the one write path in this package, so it insists on a token up
// front: an anonymous POST comes back as a 401 whose message ("Requires
// authentication") reads like a gk bug rather than a missing credential.
func (c *Client) CreatePullRequest(ctx context.Context, owner, repo string, in NewPullRequest) (PullRequest, error) {
	if c.Token == "" {
		return PullRequest{}, errors.New("creating a pull request requires a GitHub token")
	}

	payload := map[string]any{
		"title": in.Title,
		"head":  in.Head,
		"base":  in.Base,
	}
	if in.Body != "" {
		payload["body"] = in.Body
	}
	if in.Draft {
		payload["draft"] = true
	}

	resp, err := c.post(ctx, fmt.Sprintf("/repos/%s/%s/pulls", owner, repo), payload)
	if err != nil {
		return PullRequest{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		return decodePullRequest(resp.Body)
	}

	msg := apiErrorMessage(resp.Body)
	switch resp.StatusCode {
	case http.StatusUnprocessableEntity:
		// 422 is GitHub's catch-all for a rejected pull request; the prose in
		// errors[].message is the only discriminator it gives.
		lower := strings.ToLower(msg)
		switch {
		case strings.Contains(lower, "already exists"):
			return PullRequest{}, ErrPRAlreadyExists
		case strings.Contains(lower, "no commits between"):
			return PullRequest{}, ErrNoCommitsBetween
		}
		return PullRequest{}, fmt.Errorf("github rejected the pull request: %s", msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		return PullRequest{}, fmt.Errorf("github refused the request (%s): %s", resp.Status, msg)
	case http.StatusNotFound:
		// A token without repo scope sees a 404 rather than a 403, so the
		// repository name and the token's reach are equally likely causes.
		return PullRequest{}, fmt.Errorf("repository %s/%s not found or not writable with this token", owner, repo)
	default:
		return PullRequest{}, fmt.Errorf("github api returned %s: %s", resp.Status, msg)
	}
}

// FindOpenPR returns the open pull request for a head→base pair. head must be
// fully qualified as "owner:branch" (GitHub's head filter ignores a bare
// branch name on a repository that has forks). found is false when there is
// none — that is a normal answer, not an error.
//
// base is part of the filter because a branch may have several open PRs, one
// per base; only the one against this base is the PR that a create attempt
// collided with. An empty base widens the search to any base.
func (c *Client) FindOpenPR(ctx context.Context, owner, repo, head, base string) (pr PullRequest, found bool, err error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s", owner, repo, url.QueryEscape(head))
	if base != "" {
		path += "&base=" + url.QueryEscape(base)
	}
	resp, err := c.get(ctx, path)
	if err != nil {
		return PullRequest{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PullRequest{}, false, fmt.Errorf("github api returned %s: %s", resp.Status, apiErrorMessage(resp.Body))
	}

	var payload []pullRequestJSON
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return PullRequest{}, false, fmt.Errorf("decode pull request list: %w", err)
	}
	if len(payload) == 0 {
		return PullRequest{}, false, nil
	}
	return payload[0].toPullRequest(), true, nil
}

// pullRequestJSON mirrors the fields of GitHub's pull-request payload gk uses.
type pullRequestJSON struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
}

func (p pullRequestJSON) toPullRequest() PullRequest {
	return PullRequest{
		Number: p.Number,
		Title:  p.Title,
		URL:    p.HTMLURL,
		State:  p.State,
		Draft:  p.Draft,
	}
}

func decodePullRequest(r io.Reader) (PullRequest, error) {
	var payload pullRequestJSON
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return PullRequest{}, fmt.Errorf("decode pull request: %w", err)
	}
	return payload.toPullRequest(), nil
}

// apiErrorMessage renders a GitHub error body as one line. The top-level
// "message" is usually generic ("Validation Failed"); the specific reason
// lives in errors[].message, so both are joined when both are present.
func apiErrorMessage(r io.Reader) string {
	body, _ := io.ReadAll(io.LimitReader(r, 8192))
	var payload struct {
		Message string `json:"message"`
		Errors  []struct {
			Field   string `json:"field"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Message == "" {
		return strings.TrimSpace(string(body))
	}

	details := make([]string, 0, len(payload.Errors))
	for _, e := range payload.Errors {
		switch {
		case e.Message != "":
			details = append(details, e.Message)
		case e.Field != "" && e.Code != "":
			details = append(details, e.Field+" "+e.Code)
		}
	}
	if len(details) == 0 {
		return payload.Message
	}
	return payload.Message + ": " + strings.Join(details, "; ")
}
