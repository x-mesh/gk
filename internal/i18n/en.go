package i18n

// enMessages contains English message variants for normal and easy modes.
// Key naming convention: "<command>.<situation>.<item>"
//
// Every key MUST have a ModeNormal variant. ModeEasy variants are optional
// and provide beginner-friendly English descriptions with emoji.
var enMessages = map[string]map[Mode]string{
	// ── Status section headers ──────────────────────────────────────────

	"status.staged.header": {
		ModeNormal: "Staged changes",
		ModeEasy:   "+ Changes ready to be saved (staged)",
	},
	"status.unstaged.header": {
		ModeNormal: "Unstaged changes",
		ModeEasy:   "~ Changes not yet prepared (unstaged)",
	},
	"status.untracked.header": {
		ModeNormal: "Untracked files",
		ModeEasy:   "? Newly created files (untracked)",
	},
	"status.conflict.header": {
		ModeNormal: "Unmerged paths",
		ModeEasy:   "‼ Files with conflicts",
	},

	// ── Hint messages for status ────────────────────────────────────────

	"hint.status.has_staged": {
		ModeNormal: "try: gk commit",
		ModeEasy:   "→ Next step: to save your changes → gk commit",
	},
	"hint.status.has_staged.minimal": {
		ModeNormal: "gk commit",
		ModeEasy:   "gk commit",
	},
	"hint.status.has_unstaged": {
		ModeNormal: "try: git add <file>",
		ModeEasy:   "→ Next step: to prepare your changes → gk add <file>",
	},
	"hint.status.has_unstaged.minimal": {
		ModeNormal: "gk add <file>",
		ModeEasy:   "gk add <file>",
	},
	"hint.status.has_untracked": {
		ModeNormal: "try: git add <file> to track",
		ModeEasy:   "→ Next step: to start tracking a new file → gk add <file>",
	},
	"hint.status.has_untracked.minimal": {
		ModeNormal: "gk add <file>",
		ModeEasy:   "gk add <file>",
	},
	"hint.status.has_conflict": {
		ModeNormal: "fix conflicts and run: git add <file>",
		ModeEasy:   "→ Next step: fix the conflicts, then → gk add <file> → gk commit",
	},
	"hint.status.has_conflict.minimal": {
		ModeNormal: "gk add <file> && gk commit",
		ModeEasy:   "gk add <file> → gk commit",
	},

	// ── Sync hints (clean tree, depend only on ahead/behind) ────────────

	"hint.status.clean_synced": {
		ModeNormal: "in sync — nothing to do",
		ModeEasy:   "✓ Your working folder is clean and in sync with the server",
	},
	"hint.status.ahead": {
		ModeNormal: "try: gk push",
		ModeEasy:   "↑ You have %d commit(s) to upload → gk push",
	},
	"hint.status.ahead.minimal": {
		ModeNormal: "gk push",
		ModeEasy:   "gk push",
	},
	"hint.status.behind": {
		ModeNormal: "try: gk pull",
		ModeEasy:   "↓ The server has %d new commit(s) → gk pull",
	},
	"hint.status.behind.minimal": {
		ModeNormal: "gk pull",
		ModeEasy:   "gk pull",
	},
	"hint.status.diverged": {
		ModeNormal: "try: gk sync",
		ModeEasy:   "↕ Both sides have new commits (↑%d ↓%d) → gk sync",
	},
	"hint.status.diverged.minimal": {
		ModeNormal: "gk sync",
		ModeEasy:   "gk sync",
	},

	// ── Error messages ──────────────────────────────────────────────────

	"error.push_failed": {
		ModeNormal: "push failed: remote has new changes",
		ModeEasy:   "✗ Upload failed: the server has newer changes you don't have yet",
	},
	"error.pull_failed": {
		ModeNormal: "pull failed: local changes would be overwritten",
		ModeEasy:   "✗ Download failed: your local changes might be overwritten",
	},
	"error.merge_conflict": {
		ModeNormal: "merge conflict detected",
		ModeEasy:   "‼ Conflict: the same part of a file was changed differently",
	},

	// ── Error hints ─────────────────────────────────────────────────────

	"hint.error.push_failed": {
		ModeNormal: "try: gk pull first",
		ModeEasy:   "→ First, download the latest changes → gk pull",
	},
	"hint.error.push_failed.minimal": {
		ModeNormal: "gk pull",
		ModeEasy:   "gk pull",
	},
	"hint.error.pull_failed": {
		ModeNormal: "try: gk commit or gk stash first",
		ModeEasy:   "→ First, save your changes → gk commit or gk stash",
	},
	"hint.error.pull_failed.minimal": {
		ModeNormal: "gk commit or gk stash",
		ModeEasy:   "gk commit or gk stash",
	},
	"hint.error.merge_conflict": {
		ModeNormal: "fix conflicts, then: git add <file> && git commit",
		ModeEasy:   "→ Edit the conflicting files, then → gk add <file> → gk commit",
	},
	"hint.error.merge_conflict.minimal": {
		ModeNormal: "gk add <file> && gk commit",
		ModeEasy:   "gk add <file> → gk commit",
	},

	// ── General messages ────────────────────────────────────────────────

	"general.success": {
		ModeNormal: "Success",
		ModeEasy:   "✓ Success",
	},
	"general.warning": {
		ModeNormal: "Warning",
		ModeEasy:   "⚠ Warning",
	},
	"general.error": {
		ModeNormal: "Error",
		ModeEasy:   "✗ Error",
	},
	"general.nothing_to_commit": {
		ModeNormal: "nothing to commit, working tree clean",
		ModeEasy:   "✓ Nothing to save. Your working folder is clean!",
	},
	"general.branch_info": {
		ModeNormal: "On branch %s",
		ModeEasy:   "▸ Current branch: %s",
	},

	// ── Guide workflow names and descriptions ───────────────────────────

	"guide.workflow.save.name": {
		ModeNormal: "Save changes",
		ModeEasy:   "Save your changes",
	},
	"guide.workflow.save.description": {
		ModeNormal: "Stage, commit, and push your changes",
		ModeEasy:   "Edit files → save (commit) → upload to server (push)",
	},
	"guide.workflow.update.name": {
		ModeNormal: "Update from remote",
		ModeEasy:   "Get the latest code from the server",
	},
	"guide.workflow.update.description": {
		ModeNormal: "Pull latest changes from the remote repository",
		ModeEasy:   "Download the latest changes from the server into your code",
	},
	"guide.workflow.branch-work.name": {
		ModeNormal: "Branch workflow",
		ModeEasy:   "Create a branch and work on it",
	},
	"guide.workflow.branch-work.description": {
		ModeNormal: "Create a branch, work, and merge back",
		ModeEasy:   "Create a separate workspace to work independently",
	},
	"guide.workflow.resolve-conflict.name": {
		ModeNormal: "Resolve conflicts",
		ModeEasy:   "Fix conflicts",
	},
	"guide.workflow.resolve-conflict.description": {
		ModeNormal: "Fix merge conflicts and continue",
		ModeEasy:   "How to fix it when the same file was changed differently",
	},
	"guide.workflow.undo.name": {
		ModeNormal: "Undo mistakes",
		ModeEasy:   "Undo your mistakes",
	},
	"guide.workflow.undo.description": {
		ModeNormal: "Safely undo recent changes",
		ModeEasy:   "Safely undo recent mistakes",
	},

	// ── Guide step titles ───────────────────────────────────────────────

	"guide.step.check_status": {
		ModeNormal: "Check current status",
		ModeEasy:   "Check what's going on",
	},
	"guide.step.save_changes": {
		ModeNormal: "Save changes",
		ModeEasy:   "Save your changes",
	},
	"guide.step.push_to_remote": {
		ModeNormal: "Push to remote",
		ModeEasy:   "Upload to the server",
	},
	"guide.step.pull_latest": {
		ModeNormal: "Pull latest changes",
		ModeEasy:   "Download the latest code",
	},
	"guide.step.create_branch": {
		ModeNormal: "Create a new branch",
		ModeEasy:   "Create a new workspace",
	},
	"guide.step.merge_branch": {
		ModeNormal: "Merge branch",
		ModeEasy:   "Combine branches together",
	},
	"guide.step.edit_conflict": {
		ModeNormal: "Edit conflicting files",
		ModeEasy:   "Edit the files with conflicts",
	},
	"guide.step.continue_after_resolve": {
		ModeNormal: "Continue after resolving",
		ModeEasy:   "Continue after fixing",
	},
	"guide.step.undo": {
		ModeNormal: "Undo last action",
		ModeEasy:   "Undo what you just did",
	},
	"guide.step.timemachine": {
		ModeNormal: "Or use timemachine",
		ModeEasy:   "Or use the time machine",
	},

	// ── Commit messages ─────────────────────────────────────────────────

	"commit.success": {
		ModeNormal: "Committed: %s",
		ModeEasy:   "✓ Changes saved: %s",
	},
	"hint.commit.push": {
		ModeNormal: "try: gk push",
		ModeEasy:   "→ Next step: to upload to the server → gk push",
	},
	"hint.commit.push.minimal": {
		ModeNormal: "gk push",
		ModeEasy:   "gk push",
	},

	// ── Push messages ───────────────────────────────────────────────────

	"push.success": {
		ModeNormal: "Pushed to %s",
		ModeEasy:   "↑ Uploaded to server: %s",
	},

	// ── Pull messages ───────────────────────────────────────────────────

	"pull.success": {
		ModeNormal: "Pulled from %s",
		ModeEasy:   "↓ Downloaded from server: %s",
	},

	// ── Merge --into next-step hints ────────────────────────────────────

	"hint.merge.into.next_push": {
		ModeNormal: "next: gk push --from %s",
		ModeEasy:   "↑ next: upload to the server with gk push --from %s",
	},
	"hint.merge.into.next_push.minimal": {
		ModeNormal: "gk push --from %s",
		ModeEasy:   "gk push --from %s",
	},
	"hint.merge.into.cleanup_source": {
		ModeNormal: "also: gk branch delete %s (fully merged)",
		ModeEasy:   "※ cleanup: %s is fully merged — gk branch delete %s",
	},
	"hint.merge.into.cleanup_source.minimal": {
		ModeNormal: "gk branch delete %s",
		ModeEasy:   "gk branch delete %s",
	},

	// ── Push summary ────────────────────────────────────────────────────

	"hint.push.summary": {
		ModeNormal: "pushed %d commit(s) to %s/%s (%s)",
		ModeEasy:   "↑ uploaded %d commit(s) to %s/%s (%s)",
	},
	"hint.push.up_to_date": {
		ModeNormal: "up-to-date with %s/%s (%s)",
		ModeEasy:   "✓ already up-to-date with %s/%s (%s)",
	},

	// ── Status cross-worktree hints ─────────────────────────────────────

	"hint.status.cross_worktree": {
		ModeNormal: "worktree %s: %s",
		ModeEasy:   "▸ worktree %s: %s",
	},
	"hint.status.all_clean_worktrees": {
		ModeNormal: "all clean across %d worktree(s)",
		ModeEasy:   "✓ all %d worktree(s) clean and in sync",
	},

	// ── Easy Mode system messages ───────────────────────────────────────

	"easy.catalog_load_failed": {
		ModeNormal: "gk: Easy Mode catalog load failed, falling back to normal mode",
		ModeEasy:   "gk: Easy Mode catalog failed to load, switching to normal mode",
	},
	"easy.lang_not_found": {
		ModeNormal: "gk: language %q not found, falling back to English",
		ModeEasy:   "gk: language %q not found, switching to English",
	},
}

func init() {
	RegisterMessages("en", enMessages)
}
