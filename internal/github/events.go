package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RepoEvent is one flattened entry from a repository's event stream
// (GET /repos/{owner}/{repo}/events). GitHub returns a heterogeneous payload
// per Type; this struct hoists the fields the follow triggers care about, with
// the rest left zero.
type RepoEvent struct {
	ID        string // monotonically increasing numeric id (as a string)
	Type      string // PullRequestEvent | IssuesEvent | PullRequestReviewEvent | IssueCommentEvent | PushEvent | ...
	Action    string // payload.action (opened | closed | labeled | submitted | created | ...)
	Actor     string // actor.login
	CreatedAt time.Time

	// Pull-request fields (PullRequestEvent, PullRequestReviewEvent).
	PRNumber int
	PRTitle  string
	PRMerged bool
	PRBase   string // base branch ref
	PRHead   string // head branch ref

	// Issue fields (IssuesEvent, IssueCommentEvent).
	IssueNumber int
	IssueTitle  string

	Label       string // payload.label.name (labeled action)
	ReviewState string // payload.review.state (PullRequestReviewEvent)
}

// ListRepoEvents fetches the repo's recent events with a conditional ETag.
//
// On 304 Not Modified it returns notModified=true with no events (and does not
// count against the rate limit). Otherwise it returns the events newest-first
// (as GitHub sends them) and the new ETag to pass on the next call.
// pollIntervalSec echoes GitHub's X-Poll-Interval hint (0 when absent).
func (c *Client) ListRepoEvents(ctx context.Context, owner, repo, etag string) (events []RepoEvent, newETag string, notModified bool, pollIntervalSec int, err error) {
	path := fmt.Sprintf("/repos/%s/%s/events?per_page=100", owner, repo)
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase()+path, nil)
	if rerr != nil {
		return nil, etag, false, 0, rerr
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, derr := c.doer().Do(req)
	if derr != nil {
		return nil, etag, false, 0, derr
	}
	defer resp.Body.Close()

	pollIntervalSec = atoiSafe(resp.Header.Get("X-Poll-Interval"))

	if resp.StatusCode == http.StatusNotModified {
		// Keep the previous ETag — GitHub omits it on 304.
		return nil, etag, true, pollIntervalSec, nil
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, etag, false, pollIntervalSec, rateLimitError(resp)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, etag, false, pollIntervalSec, fmt.Errorf("github events returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	newETag = resp.Header.Get("ETag")

	var raw []struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Actor struct {
			Login string `json:"login"`
		} `json:"actor"`
		CreatedAt string          `json:"created_at"`
		Payload   json.RawMessage `json:"payload"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&raw); derr != nil {
		return nil, newETag, false, pollIntervalSec, fmt.Errorf("decode events: %w", derr)
	}

	events = make([]RepoEvent, 0, len(raw))
	for _, r := range raw {
		ev := RepoEvent{ID: r.ID, Type: r.Type, Actor: r.Actor.Login}
		if t, terr := time.Parse(time.RFC3339, r.CreatedAt); terr == nil {
			ev.CreatedAt = t
		}
		applyEventPayload(&ev, r.Payload)
		events = append(events, ev)
	}
	return events, newETag, false, pollIntervalSec, nil
}

// eventPayload is the union of the payload fields the triggers read. JSON
// decoding ignores keys not present for a given event Type, so one struct
// covers every type.
type eventPayload struct {
	Action      string `json:"action"`
	PullRequest *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Merged bool   `json:"merged"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	Issue *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	} `json:"issue"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label"`
	Review *struct {
		State string `json:"state"`
	} `json:"review"`
}

func applyEventPayload(ev *RepoEvent, raw json.RawMessage) {
	var p eventPayload
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	ev.Action = p.Action
	if p.PullRequest != nil {
		ev.PRNumber = p.PullRequest.Number
		ev.PRTitle = p.PullRequest.Title
		ev.PRMerged = p.PullRequest.Merged
		ev.PRBase = p.PullRequest.Base.Ref
		ev.PRHead = p.PullRequest.Head.Ref
	}
	if p.Issue != nil {
		ev.IssueNumber = p.Issue.Number
		ev.IssueTitle = p.Issue.Title
	}
	if p.Label != nil {
		ev.Label = p.Label.Name
	}
	if p.Review != nil {
		ev.ReviewState = p.Review.State
	}
}

// EventIDLess reports whether event id a is older than b. GitHub event ids are
// monotonically increasing numeric strings; it parses them as integers and
// falls back to length-then-lexical comparison for any non-numeric id.
func EventIDLess(a, b string) bool {
	ai, aerr := strconv.ParseInt(a, 10, 64)
	bi, berr := strconv.ParseInt(b, 10, 64)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
