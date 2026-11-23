package github

import (
	"context"
	"fmt"
	"time"
)

const (
	// unreleasedBranch is compared with the latest tag in addition to the
	// default branch: work often waits for a release on it.
	unreleasedBranch = "develop"
	// unreleasedWindow is how many of the newest commits one comparison
	// returns. With 20 repositories and two comparisons each the query cost
	// 13 points and took 1.6 to 2.6 s on a 40-repository account, and 3.6 to
	// 8.7 s on kubernetes, golang, and vercel.
	unreleasedWindow = 30
)

// CompareCommit is one commit of a comparison. Parent is its first parent.
type CompareCommit struct {
	OID         string
	Parent      string
	Message     string
	CommittedAt time.Time
}

// TagComparison is a branch compared with the latest tag: Ahead commits are
// on the branch and not in the tag, and Behind commits are in the tag and not
// on the branch. Behind is above 0 on unreleasedBranch after every release
// that leaves a commit on the default branch only, so it says that the tag is
// no release of this repository only when the default branch has it too.
// Commits holds the newest unreleasedWindow of the Ahead commits, the branch
// head last.
type TagComparison struct {
	Branch  string
	Default bool // the repository's default branch
	Ahead   int
	Behind  int
	Head    string
	Commits []CompareCommit
}

// RepoUnreleased is one repository with a tag: its latest tag by commit date,
// and the default branch and unreleasedBranch compared with it, the default
// branch first. A branch the repository does not have is left out.
type RepoUnreleased struct {
	Repo     string // owner/name
	Tag      string
	Branches []TagComparison
}

// OwnerUnreleased is one read of an owner's repositories: Total is how many
// the owner has, Scanned how many were read (the newest push first), and
// Repos those of the read ones that have a tag.
type OwnerUnreleased struct {
	Total   int
	Scanned int
	Repos   []RepoUnreleased
	Notes   []string
}

const unreleasedQuery = `fragment C on Comparison {
  aheadBy behindBy
  headTarget { oid }
  commits(last: %[2]d) { nodes { oid message committedDate parents(first: 1) { nodes { oid } } } }
}
query($owner: String!) {
  repositoryOwner(login: $owner) {
    repositories(first: %[1]d, orderBy: {field: PUSHED_AT, direction: DESC}, isArchived: false, isFork: false, ownerAffiliations: OWNER) {
      totalCount
      nodes {
        nameWithOwner
        defaultBranchRef { name }
        tags: refs(refPrefix: "refs/tags/", first: 1, orderBy: {field: TAG_COMMIT_DATE, direction: DESC}) {
          nodes {
            name
            head: compare(headRef: "HEAD") { ...C }
            dev: compare(headRef: %[3]q) { ...C }
          }
        }
      }
    }
  }
}
`

type unreleasedCompare struct {
	AheadBy    int `json:"aheadBy"`
	BehindBy   int `json:"behindBy"`
	HeadTarget struct {
		Oid string `json:"oid"`
	} `json:"headTarget"`
	Commits struct {
		Nodes []struct {
			Oid           string    `json:"oid"`
			Message       string    `json:"message"`
			CommittedDate time.Time `json:"committedDate"`
			Parents       struct {
				Nodes []struct {
					Oid string `json:"oid"`
				} `json:"nodes"`
			} `json:"parents"`
		} `json:"nodes"`
	} `json:"commits"`
}

func (c *unreleasedCompare) comparison(branch string, isDefault bool) TagComparison {
	t := TagComparison{Branch: branch, Default: isDefault, Ahead: c.AheadBy, Behind: c.BehindBy, Head: c.HeadTarget.Oid}
	for _, n := range c.Commits.Nodes {
		cm := CompareCommit{OID: n.Oid, Message: n.Message, CommittedAt: n.CommittedDate}
		if len(n.Parents.Nodes) > 0 {
			cm.Parent = n.Parents.Nodes[0].Oid
		}
		t.Commits = append(t.Commits, cm)
	}
	return t
}

type unreleasedData struct {
	RepositoryOwner *struct {
		Repositories struct {
			TotalCount int `json:"totalCount"`
			Nodes      []struct {
				NameWithOwner    string `json:"nameWithOwner"`
				DefaultBranchRef *struct {
					Name string `json:"name"`
				} `json:"defaultBranchRef"`
				Tags struct {
					Nodes []struct {
						Name string             `json:"name"`
						Head *unreleasedCompare `json:"head"`
						Dev  *unreleasedCompare `json:"dev"`
					} `json:"nodes"`
				} `json:"tags"`
			} `json:"nodes"`
		} `json:"repositories"`
	} `json:"repositoryOwner"`
}

// OwnerUnreleased reads, with one GraphQL query, the latest tag of the
// owner's most recently pushed repositories and the commits each branch has
// after it. Forks and archived repositories are left out. A repository
// without unreleasedBranch answers that comparison with NOT_FOUND, which is
// expected and not a note.
func (c *Client) OwnerUnreleased(ctx context.Context, owner string) (*OwnerUnreleased, error) {
	if c.Token == "" {
		return nil, ErrNoToken
	}
	query := fmt.Sprintf(unreleasedQuery, dashPage, unreleasedWindow, unreleasedBranch)
	noBranch := func(e graphqlError) bool {
		if e.Type != "NOT_FOUND" || len(e.Path) == 0 {
			return false
		}
		last, _ := e.Path[len(e.Path)-1].(string)
		return last == "dev"
	}
	var data unreleasedData
	notes, err := c.graphqlSkip(ctx, "unreleased", query, map[string]any{"owner": owner}, &data, noBranch)
	if err != nil {
		return nil, err
	}
	if data.RepositoryOwner == nil {
		return nil, fmt.Errorf("github unreleased query: owner %q not found", owner)
	}
	repos := data.RepositoryOwner.Repositories
	res := &OwnerUnreleased{Total: repos.TotalCount, Scanned: len(repos.Nodes), Notes: notes}
	for _, n := range repos.Nodes {
		if len(n.Tags.Nodes) == 0 {
			continue
		}
		tag := n.Tags.Nodes[0]
		r := RepoUnreleased{Repo: n.NameWithOwner, Tag: tag.Name}
		def := ""
		if n.DefaultBranchRef != nil {
			def = n.DefaultBranchRef.Name
		}
		if tag.Head != nil && def != "" {
			r.Branches = append(r.Branches, tag.Head.comparison(def, true))
		}
		if tag.Dev != nil && def != unreleasedBranch {
			r.Branches = append(r.Branches, tag.Dev.comparison(unreleasedBranch, false))
		}
		res.Repos = append(res.Repos, r)
	}
	return res, nil
}
