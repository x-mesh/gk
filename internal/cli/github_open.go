package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/ui"
)

// openBrowser launches the OS default browser for target, without waiting for
// it to exit. A spawn failure (no opener on PATH) is returned so the caller can
// fall back to printing the URL.
func openBrowser(target string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "open"
	case "windows":
		name, args = "cmd", []string{"/c", "start"}
	default:
		name = "xdg-open"
	}
	args = append(args, target)
	return exec.Command(name, args...).Start()
}

// gitHubSearchURL turns a search API `q=` into the equivalent github.com web
// search URL, picking the pull-requests tab when the query is PR-scoped.
func gitHubSearchURL(query string) string {
	typ := "issues"
	if strings.Contains(query, "is:pr") {
		typ = "pullrequests"
	}
	return "https://github.com/search?q=" + url.QueryEscape(query) + "&type=" + typ
}

// openGitHubSearch backs --web: open the query as a github.com search. On a
// spawn failure it prints the URL so the user can open it by hand.
func openGitHubSearch(cmd *cobra.Command, query string) error {
	u := gitHubSearchURL(query)
	if err := openBrowser(u); err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), u) // fallback: emit the URL to open manually
		return nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "opening "+u)
	return nil
}

// pickGitHubItem backs --pick: choose one item interactively, then open its URL
// in the browser. An aborted picker (Esc/Ctrl-C) is a quiet no-op.
func pickGitHubItem(cmd *cobra.Command, issues []ghapi.Issue) error {
	if len(issues) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no items to pick")
		return nil
	}
	items := make([]ui.PickerItem, 0, len(issues))
	for _, is := range issues {
		typ := "issue"
		if is.IsPR {
			typ = "PR"
		}
		slug := fmt.Sprintf("%s/%s#%d", is.Owner, is.Repo, is.Number)
		items = append(items, ui.PickerItem{
			Display: fmt.Sprintf("%-6s %-24s %s", typ, slug, is.Title),
			Key:     is.URL,
			Cells:   []string{typ, slug, is.Title},
		})
	}
	picked, err := ui.NewPicker().Pick(cmdCtx(cmd), "open which item?", items)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return nil
		}
		return err
	}
	if err := openBrowser(picked.Key); err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), picked.Key)
		return nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "opening "+picked.Key)
	return nil
}
