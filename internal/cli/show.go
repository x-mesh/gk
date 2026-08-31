package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/diff"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

const defaultShowLimit = 20

type showCommit struct {
	SHA, ShortSHA, Author, Email, Date, Parents, Subject, Body string
	Patch                                                      string
	Diff                                                       *diff.DiffResult
}

type showCommitJSON struct {
	SHA      string `json:"sha"`
	ShortSHA string `json:"short_sha"`
	Author   string `json:"author"`
	Email    string `json:"email"`
	Date     string `json:"date"`
	Parents  string `json:"parents,omitempty"`
	Subject  string `json:"subject"`
	Body     string `json:"body,omitempty"`
}

type showJSON struct {
	Schema int            `json:"schema"`
	Commit showCommitJSON `json:"commit"`
	Diff   *diff.DiffJSON `json:"diff,omitempty"`
	Patch  string         `json:"patch,omitempty"`
}

func init() {
	cmd := &cobra.Command{
		Use:   "show [<commit>]",
		Short: "Show one commit or browse recent commits interactively",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runShow,
	}
	cmd.Flags().Bool("stat", false, "show changed files and statistics")
	cmd.Flags().Bool("name-only", false, "show changed file paths only")
	cmd.Flags().Bool("no-patch", false, "hide the patch body")
	cmd.Flags().Bool("patch", false, "show the patch body")
	cmd.Flags().IntP("limit", "n", defaultShowLimit, "number of commits to load per page")
	rootCmd.AddCommand(cmd)
}

func runShow(cmd *cobra.Command, args []string) error {
	stat, _ := cmd.Flags().GetBool("stat")
	nameOnly, _ := cmd.Flags().GetBool("name-only")
	noPatch, _ := cmd.Flags().GetBool("no-patch")
	patchOnly, _ := cmd.Flags().GetBool("patch")
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 {
		return fmt.Errorf("gk show: --limit must be at least 1")
	}
	if stat && nameOnly {
		return fmt.Errorf("gk show: --stat and --name-only cannot be combined")
	}
	if noPatch && patchOnly {
		return fmt.Errorf("gk show: --no-patch and --patch cannot be combined")
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	if len(args) == 0 {
		commits, err := loadShowCommits(cmd.Context(), runner, limit)
		if err != nil {
			return err
		}
		if JSONOut() || AgentOut() {
			return writeShowListJSON(cmd.OutOrStdout(), commits)
		}
		if ui.IsTerminal() {
			return runShowTUI(cmd.Context(), commits, runner, limit, stat, nameOnly, noPatch, patchOnly)
		}
		return writeShowList(cmd.OutOrStdout(), commits)
	}
	commit, err := loadShowCommit(cmd.Context(), runner, args[0])
	if err != nil {
		return err
	}
	if JSONOut() {
		return writeShowJSON(cmd.OutOrStdout(), commit, !noPatch || patchOnly)
	}
	return writeShowText(cmd.OutOrStdout(), commit, stat, nameOnly, noPatch, patchOnly)
}

const showRecordFormat = "%H%x00%h%x00%an%x00%ae%x00%aI%x00%P%x00%s%x00%b%x1e"

func parseShowRecords(raw []byte) []showCommit {
	records := strings.Split(strings.TrimRight(string(raw), "\x1e\n"), "\x1e")
	commits := make([]showCommit, 0, len(records))
	for _, record := range records {
		fields := strings.SplitN(strings.TrimLeft(record, "\x00\n"), "\x00", 8)
		if len(fields) < 8 || fields[0] == "" {
			continue
		}
		commits = append(commits, showCommit{
			SHA: fields[0], ShortSHA: fields[1], Author: fields[2], Email: fields[3],
			Date: fields[4], Parents: fields[5], Subject: fields[6], Body: strings.TrimRight(fields[7], "\n"),
		})
	}
	return commits
}

func loadShowCommits(ctx context.Context, runner git.Runner, limit int) ([]showCommit, error) {
	return loadShowCommitsPage(ctx, runner, limit, 0, "")
}

func loadShowCommitsPage(ctx context.Context, runner git.Runner, limit, skip int, base string) ([]showCommit, error) {
	args := []string{"log", "-n", strconv.Itoa(limit), "--skip=" + strconv.Itoa(skip), "--date=iso-strict", "--format=" + showRecordFormat}
	if base != "" {
		args = append(args, base)
	}
	out, stderr, err := runner.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("gk show: git log failed: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return parseShowRecords(out), nil
}

func loadShowCommit(ctx context.Context, runner git.Runner, ref string) (showCommit, error) {
	out, stderr, err := runner.Run(ctx, "show", "-s", "--date=iso-strict", "--format="+showRecordFormat, ref+"^{commit}")
	if err != nil {
		message := strings.TrimSpace(string(stderr))
		if message == "" {
			message = err.Error()
		}
		return showCommit{}, fmt.Errorf("gk show: commit을 찾을 수 없습니다: %s", message)
	}
	commits := parseShowRecords(out)
	if len(commits) != 1 {
		return showCommit{}, fmt.Errorf("gk show: commit metadata를 읽을 수 없습니다: %s", ref)
	}
	commit := commits[0]
	patch, stderr, err := runner.Run(ctx, "show", "--format=", "--root", "--no-ext-diff", "--no-color", "--find-renames", ref+"^{commit}")
	if err != nil {
		return showCommit{}, fmt.Errorf("gk show: patch를 읽을 수 없습니다: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	commit.Patch = string(patch)
	parsed, parseErr := diff.ParseUnifiedDiff(bytes.NewReader(patch))
	if parseErr != nil {
		parsed = &diff.DiffResult{}
	}
	commit.Diff = parsed
	return commit, nil
}

func commitJSON(commit showCommit) showCommitJSON {
	return showCommitJSON{SHA: commit.SHA, ShortSHA: commit.ShortSHA, Author: commit.Author, Email: commit.Email, Date: commit.Date, Parents: commit.Parents, Subject: commit.Subject, Body: commit.Body}
}

func writeShowJSON(w io.Writer, commit showCommit, includePatch bool) error {
	payload := showJSON{Schema: 1, Commit: commitJSON(commit)}
	if includePatch {
		payload.Diff = diff.ToJSON(commit.Diff)
	}
	if includePatch {
		payload.Patch = commit.Patch
	}
	return emitAgentResult(w, payload)
}

type showListJSON struct {
	Schema  int              `json:"schema"`
	Commits []showCommitJSON `json:"commits"`
}

func writeShowListJSON(w io.Writer, commits []showCommit) error {
	items := make([]showCommitJSON, 0, len(commits))
	for _, commit := range commits {
		items = append(items, commitJSON(commit))
	}
	return emitAgentResult(w, showListJSON{Schema: 1, Commits: items})
}

func writeShowText(w io.Writer, commit showCommit, stat, nameOnly, noPatch, patchOnly bool) error {
	if !stat && !nameOnly && !noPatch && !patchOnly {
		stat = true
		patchOnly = true
	}
	fmt.Fprintf(w, "commit %s\nAuthor: %s <%s>\nDate:   %s\n", commit.SHA, commit.Author, commit.Email, commit.Date)
	if commit.Parents != "" {
		fmt.Fprintf(w, "Parent: %s\n", commit.Parents)
	}
	fmt.Fprintf(w, "\n    %s\n", commit.Subject)
	if commit.Body != "" {
		for _, line := range strings.Split(commit.Body, "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	if noPatch {
		return nil
	}
	if stat {
		if err := diff.RenderStat(w, commit.Diff, NoColorFlag()); err != nil {
			return fmt.Errorf("gk show: stat 렌더링 실패: %w", err)
		}
	}
	if nameOnly {
		for _, file := range commit.Diff.Files {
			path := file.NewPath
			if file.Status == diff.StatusDeleted {
				path = file.OldPath
			}
			fmt.Fprintln(w, path)
		}
	}
	if (stat || nameOnly) && !patchOnly {
		return nil
	}
	if strings.TrimSpace(commit.Patch) == "" {
		return nil
	}
	if len(commit.Diff.Files) > 0 {
		return diff.Render(w, commit.Diff, diff.RenderOptions{NoColor: NoColorFlag(), Context: 3, ShowRefs: false})
	}
	_, err := io.WriteString(w, commit.Patch)
	return err
}

func writeShowList(w io.Writer, commits []showCommit) error {
	for _, commit := range commits {
		fmt.Fprintf(w, "%s (%s) <%s> %s\n", commit.ShortSHA, commit.Date, commit.Author, commit.Subject)
	}
	return nil
}

type showBrowserModel struct {
	commits       []showCommit
	runner        git.Runner
	ctx           context.Context
	base          string
	pageSize      int
	nextSkip      int
	hasMore       bool
	loadingMore   bool
	details       map[string]showCommit
	loadingDetail bool
	detailRequest string
	detailError   string
	selected      int
	focusDetail   bool
	width         int
	leftWidth     int
	list          viewport.Model
	detail        viewport.Model
	ready         bool
	quitting      bool
	stat          bool
	nameOnly      bool
	noPatch       bool
	patchOnly     bool
}

type showDetailLoadedMsg struct {
	sha    string
	commit showCommit
	err    error
}

type showMoreLoadedMsg struct {
	commits []showCommit
	err     error
}

func (m showBrowserModel) Init() tea.Cmd { return nil }

func (m showBrowserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case showDetailLoadedMsg:
		if msg.err != nil {
			if msg.sha == m.detailRequest {
				m.loadingDetail = false
				m.detailError = msg.err.Error()
			}
		} else {
			m.details[msg.commit.SHA] = msg.commit
			if msg.sha == m.detailRequest {
				m.loadingDetail = false
				m.detailError = ""
			}
		}
		m.updateDetail()
		if len(m.commits) > 0 && m.selected < len(m.commits) && m.commits[m.selected].SHA != msg.sha {
			if msg.sha == m.detailRequest {
				m.loadingDetail = false
			}
			return m, m.loadSelectedDetail()
		}
		return m, nil
	case showMoreLoadedMsg:
		m.loadingMore = false
		if msg.err != nil {
			m.detailError = msg.err.Error()
			return m, nil
		}
		m.commits = append(m.commits, msg.commits...)
		m.nextSkip += len(msg.commits)
		m.hasMore = len(msg.commits) == m.pageSize
		m.updateList()
		m.updateDetail()
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyTab:
			m.focusDetail = !m.focusDetail
			return m, nil
		case tea.KeyUp:
			if !m.focusDetail && m.selected > 0 {
				m.selected--
				m.updateList()
				return m, m.loadSelectedDetail()
			}
		case tea.KeyDown:
			if !m.focusDetail {
				if m.selected+1 < len(m.commits) {
					m.selected++
					m.updateList()
					return m, m.loadSelectedDetail()
				}
				return m, m.loadMore()
			}
		}
		if !m.focusDetail && (msg.String() == "m" || msg.Type == tea.KeyPgDown) {
			return m, m.loadMore()
		}
		if msg.String() == "q" {
			m.quitting = true
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.layout(msg.Height)
		return m, m.loadSelectedDetail()
	}
	if m.focusDetail {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *showBrowserModel) layout(height int) {
	if m.width >= 80 {
		m.leftWidth = m.width / 3
		detailWidth := m.width - m.leftWidth - 3
		if detailWidth < 20 {
			detailWidth = 20
		}
		m.setPanelSizes(height, detailWidth)
	} else {
		m.leftWidth = m.width
		detailWidth := m.width - 4
		if detailWidth < 1 {
			detailWidth = 1
		}
		m.setPanelSizes(height, detailWidth)
	}
	if m.width >= 80 {
		if m.list.Width < 8 {
			m.list.Width = 8
		}
		if m.detail.Width < 12 {
			m.detail.Width = 12
		}
	} else {
		if m.list.Width < 1 {
			m.list.Width = 1
		}
		if m.detail.Width < 1 {
			m.detail.Width = 1
		}
	}
	m.updateList()
	m.updateDetail()
}

func (m *showBrowserModel) setPanelSizes(height, detailWidth int) {
	listHeight := height - 4
	detailHeight := height - 4
	if m.width < 80 {
		usable := height - 5
		if usable < 0 {
			usable = 0
		}
		listHeight = usable / 3
		detailHeight = usable - listHeight
	}
	if listHeight < 0 {
		listHeight = 0
	}
	if detailHeight < 0 {
		detailHeight = 0
	}
	if !m.ready {
		m.list = viewport.New(m.leftWidth-4, listHeight)
		m.detail = viewport.New(detailWidth, detailHeight)
		m.ready = true
	} else {
		m.list.Width, m.list.Height = m.leftWidth-4, listHeight
		m.detail.Width, m.detail.Height = detailWidth, detailHeight
	}
}

func (m *showBrowserModel) updateList() {
	if !m.ready {
		return
	}
	var content strings.Builder
	content.WriteString("Commits\n")
	for i, commit := range m.commits {
		line := fmt.Sprintf("%s %s", commit.ShortSHA, commit.Subject)
		if i == m.selected {
			line = "▶ " + line
		}
		content.WriteString(line)
		content.WriteByte('\n')
	}
	m.list.SetContent(strings.TrimRight(content.String(), "\n"))
	line := m.selected + 1
	if line < m.list.YOffset {
		m.list.SetYOffset(line)
	} else if line >= m.list.YOffset+m.list.Height {
		m.list.SetYOffset(line - m.list.Height + 1)
	}
}

func (m *showBrowserModel) updateDetail() {
	if !m.ready || len(m.commits) == 0 {
		return
	}
	commit := m.commits[m.selected]
	if loaded, ok := m.details[commit.SHA]; ok {
		commit = loaded
	}
	var body bytes.Buffer
	if m.loadingDetail && commit.Patch == "" {
		fmt.Fprintf(&body, "commit %s\n\nloading commit details…", commit.SHA)
	} else if m.detailError != "" {
		fmt.Fprintf(&body, "commit %s\n\n%s", commit.SHA, m.detailError)
	} else {
		_ = writeShowText(&body, commit, m.stat, m.nameOnly, m.noPatch, m.patchOnly)
	}
	m.detail.SetContent(body.String())
	m.detail.GotoTop()
}

func (m *showBrowserModel) loadSelectedDetail() tea.Cmd {
	if len(m.commits) == 0 || m.selected >= len(m.commits) {
		return nil
	}
	commit := m.commits[m.selected]
	if _, ok := m.details[commit.SHA]; ok || m.loadingDetail {
		return nil
	}
	m.loadingDetail = true
	m.detailRequest = commit.SHA
	m.detailError = ""
	m.updateDetail()
	return func() tea.Msg {
		loaded, err := loadShowCommit(m.ctx, m.runner, commit.SHA)
		return showDetailLoadedMsg{sha: commit.SHA, commit: loaded, err: err}
	}
}

func (m *showBrowserModel) loadMore() tea.Cmd {
	if m.loadingMore || !m.hasMore {
		return nil
	}
	m.loadingMore = true
	return func() tea.Msg {
		commits, err := loadShowCommitsPage(m.ctx, m.runner, m.pageSize, m.nextSkip, m.base)
		return showMoreLoadedMsg{commits: commits, err: err}
	}
}

func (m showBrowserModel) View() string {
	if !m.ready || len(m.commits) == 0 {
		return "loading…"
	}
	faint := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	listStyle := lipgloss.NewStyle().Width(m.leftWidth).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("99")).Padding(0, 1)
	leftBox := listStyle.Render(m.list.View())
	rightBox := lipgloss.NewStyle().Width(m.detail.Width).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("99")).Render(m.detail.View())
	status := fmt.Sprintf("%d commits loaded", len(m.commits))
	if m.loadingMore {
		status += " · loading more…"
	} else if m.hasMore {
		status += " · m/PageDown load more"
	} else {
		status += " · end of history"
	}
	help := faint.Render(status + " · ↑/↓ select · tab detail · q/esc quit")
	if m.width >= 80 {
		return lipgloss.JoinHorizontal(lipgloss.Top, leftBox, " ", rightBox) + "\n" + help
	}
	return leftBox + "\n" + rightBox + "\n" + help
}

func runShowTUI(ctx context.Context, commits []showCommit, runner git.Runner, limit int, stat, nameOnly, noPatch, patchOnly bool) error {
	baseBytes, _, err := runner.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("gk show: HEAD를 고정할 수 없습니다: %w", err)
	}
	base := strings.TrimSpace(string(baseBytes))
	if !stat && !nameOnly && !noPatch && !patchOnly {
		stat = true
		patchOnly = true
	}
	model := showBrowserModel{
		commits: commits, runner: runner, ctx: ctx, base: base,
		pageSize: limit, nextSkip: len(commits), hasMore: len(commits) == limit,
		details: make(map[string]showCommit),
		stat:    stat, nameOnly: nameOnly, noPatch: noPatch, patchOnly: patchOnly,
	}
	prog := tea.NewProgram(model, tea.WithContext(ctx), tea.WithOutput(os.Stderr), tea.WithInputTTY(), tea.WithAltScreen())
	_, err = prog.Run()
	if err != nil {
		return fmt.Errorf("gk show: interactive browser: %w", err)
	}
	return nil
}
