package ui

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"fcards/internal/api"
	"fcards/internal/store"
	"fcards/internal/sync"
)

type screen int

const (
	screenLogin screen = iota
	screenDashboard
	screenReview
	screenBrowse
	screenStats
)

// App is the root bubbletea model.
type App struct {
	theme  Theme
	styles Styles

	client *api.Client
	cfg    *store.Config
	svc    *sync.Service

	screen  screen
	width   int
	height  int
	errMsg  string
	status  string
	loading bool

	login     LoginModel
	dashboard DashboardModel
	review    ReviewModel
	browse    BrowseModel
	stats     StatsModel

	showHelp      bool
	showWorkspace bool
	workspaceList []api.Workspace
	workspaceCur  int

	showTags  bool
	tagPicker TagPicker

	debugReview bool
	offline     bool
	queued      int
}

type tickMsg struct{}

type loginSuccessMsg struct{}
type refreshDoneMsg struct{ err error }
type outboxDrainedMsg struct {
	applied   int
	remaining int
}

func offlineTick() tea.Cmd {
	return tea.Tick(90*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

// isOfflineErr reports whether an error is a transport failure (as opposed to
// a structured API error from the service).
func isOfflineErr(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*api.APIError); ok {
		return false
	}
	var ue *url.Error
	return errors.As(err, &ue) || errors.Is(err, os.ErrDeadlineExceeded)
}

func NewApp(cfg *store.Config, client *api.Client, svc *sync.Service) *App {
	theme := LoadTheme()
	styles := NewStyles(theme)
	app := &App{
		theme: theme, styles: styles,
		client: client, cfg: cfg, svc: svc,
		screen: screenDashboard,
	}
	if cfg.APIKey == "" {
		app.screen = screenLogin
	}
	app.login = NewLogin(cfg, client, theme, styles)
	app.dashboard = NewDashboard(theme, styles)
	app.review = NewReview(theme, styles)
	app.browse = NewBrowse(theme, styles)
	app.stats = NewStats(theme, styles)
	app.tagPicker = NewTagPicker(theme, styles)
	return app
}

func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.SetWindowTitle("fcards"), textinput.Blink}
	// Debug aid: FCARDS_DEBUG=review opens a revealed review card after sync.
	if os.Getenv("FCARDS_DEBUG") == "review" && a.svc != nil {
		a.screen = screenReview
		a.debugReview = true
	}
	if a.svc != nil {
		cmds = append(cmds, a.refreshCmd())
	}
	cmds = append(cmds, offlineTick())
	return tea.Batch(cmds...)
}

func (a *App) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		if a.svc == nil {
			return refreshDoneMsg{}
		}
		// First sync on a fresh login: pick a workspace so requests are scoped.
		if a.cfg.WorkspaceID == "" {
			if ws, err := a.client.Workspaces(); err == nil && len(ws) > 0 {
				w := ws[0]
				for _, cand := range ws {
					if cand.SelectedNow() {
						w = cand
					}
				}
				a.cfg.WorkspaceID = w.ID()
				a.cfg.WorkspaceName = w.Name
				_ = store.SaveConfig(a.cfg)
			}
		}
		err := a.svc.Refresh()
		return refreshDoneMsg{err}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil

	case loginSuccessMsg:
		a.client.APIKey = a.cfg.APIKey
		if a.svc == nil && a.cfg.APIKey != "" {
			a.svc = sync.New(a.client, a.cfg, a.cfg.ReplicaID, a.cfg.InstallationID)
		}
		a.screen = screenDashboard
		a.loading = true
		return a, tea.Batch(a.refreshCmd(), a.drainOutboxCmd())

	case refreshDoneMsg:
		a.loading = false
		if msg.err != nil {
			if isOfflineErr(msg.err) {
				a.offline = true
				a.errMsg = ""
				a.status = ""
				a.queued = store.OutboxCount()
			} else {
				a.errMsg = msg.err.Error()
			}
		} else {
			a.offline = false
			a.errMsg = ""
			a.status = "synced " + timeNow().Format("15:04:05")
		}
		a.dashboard.UpdateData(a.svc)
		a.review.SetCards(a.svc)
		a.browse.SetCards(a.svc)
		a.stats.UpdateData(a.svc)
		if a.debugReview && a.svc != nil {
			a.debugReview = false
			a.review.Begin(a.svc)
			a.review.revealed = true
			return a, imageKick()
		}
		return a, nil

	case tickMsg:
		if a.svc != nil && a.queued > 0 {
			return a, tea.Batch(a.drainOutboxCmd(), offlineTick())
		}
		return a, offlineTick()

	case outboxDrainedMsg:
		a.queued = msg.remaining
		if msg.applied > 0 {
			a.status = fmt.Sprintf("flushed %d queued review(s)", msg.applied)
			return a, tea.Batch(a.refreshCmd(), offlineTick())
		}
		if msg.remaining > 0 {
			a.offline = true
		} else if a.queued == 0 {
			a.offline = false
		}
		return a, nil

	case reviewFinishedMsg:
		a.dashboard.UpdateData(a.svc)
		a.stats.UpdateData(a.svc)
		a.status = "review saved"
		if msg.err != nil {
			if strings.Contains(msg.err.Error(), "queued offline") {
				a.offline = true
				a.errMsg = ""
				a.status = "review queued offline"
			} else {
				a.errMsg = msg.err.Error()
			}
		}
		a.queued = store.OutboxCount()
		return a, nil
	}

	// Global keys
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return a, tea.Quit
		case "?":
			if a.screen != screenLogin {
				a.showHelp = !a.showHelp
			}
			return a, nil
		case "esc":
			if a.showHelp {
				a.showHelp = false
				return a, nil
			}
			if a.showTags {
				a.showTags = false
				return a, nil
			}
			if a.showWorkspace {
				a.showWorkspace = false
				return a, nil
			}
		case "R":
			if a.svc != nil && !a.showHelp && !a.showWorkspace && a.screen != screenReview {
				a.loading = true
				return a, a.refreshCmd()
			}
		}
	}

	if a.showHelp {
		return a, nil
	}

	if a.showTags {
		return a.updateTagPicker(msg)
	}

	if a.showWorkspace {
		return a.updateWorkspaceOverlay(msg)
	}

	switch a.screen {
	case screenLogin:
		return a.updateLogin(msg)
	case screenDashboard:
		return a.updateDashboard(msg)
	case screenReview:
		return a.updateReview(msg)
	case screenBrowse:
		return a.updateBrowse(msg)
	case screenStats:
		return a.updateStats(msg)
	}
	return a, nil
}

func (a *App) drainOutboxCmd() tea.Cmd {
	return func() tea.Msg {
		applied, remaining, err := a.svc.DrainOutbox()
		if err != nil {
			return outboxDrainedMsg{remaining: -1}
		}
		return outboxDrainedMsg{applied: applied, remaining: remaining}
	}
}

func (a *App) updateTagPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := a.tagPicker.Update(msg)
	a.tagPicker = m
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" {
		a.dashboard.UpdateData(a.svc)
		a.status = a.tagPicker.statusMsg
	}
	return a, cmd
}

func (a *App) updateWorkspaceOverlay(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "up", "k":
			if a.workspaceCur > 0 {
				a.workspaceCur--
			}
		case "down", "j":
			if a.workspaceCur < len(a.workspaceList)-1 {
				a.workspaceCur++
			}
		case "enter":
			if a.workspaceCur < len(a.workspaceList) {
				w := a.workspaceList[a.workspaceCur]
				a.cfg.WorkspaceID = w.ID()
				a.cfg.WorkspaceName = w.Name
				_ = store.SaveConfig(a.cfg)
				a.svc.Config = a.cfg
				a.svc.SwitchWorkspace(w.ID())
				a.showWorkspace = false
				a.loading = true
				return a, a.refreshCmd()
			}
		}
	}
	return a, nil
}

func (a *App) openWorkspaceOverlay() tea.Cmd {
	return func() tea.Msg {
		ws, err := a.client.Workspaces()
		if err != nil {
			a.errMsg = err.Error()
			return nil
		}
		if len(ws) == 0 {
			a.errMsg = "no workspaces found"
			return nil
		}
		a.workspaceList = ws
		a.workspaceCur = 0
		a.showWorkspace = true
		return nil
	}
}

func (a *App) View() string {
	var b strings.Builder

	if a.screen != screenLogin {
		b.WriteString(a.renderHeader())
		b.WriteString("\n")
	}

	var body string
	switch a.screen {
	case screenLogin:
		body = a.login.View(a.width, a.height)
	case screenDashboard:
		body = a.dashboard.View(a.width, a.height)
	case screenReview:
		body = a.review.View(a.width, a.height)
	case screenBrowse:
		body = a.browse.View(a.width, a.height)
	case screenStats:
		body = a.stats.View(a.width, a.height)
	}
	b.WriteString(body)

	if a.errMsg != "" {
		b.WriteString("\n")
		b.WriteString(a.styles.Error.Render("  ! " + a.errMsg))
	}
	if a.showTags {
		b.WriteString("\n")
		b.WriteString(a.styles.Border.Render(a.tagPicker.View(a.width, a.height-4)))
	}
	if a.showWorkspace {
		b.WriteString("\n")
		b.WriteString(a.renderWorkspaceOverlay())
	}
	if a.showHelp {
		b.WriteString("\n")
		b.WriteString(a.renderHelp())
	}
	if a.screen != screenLogin {
		b.WriteString("\n")
		b.WriteString(a.renderFooter())
	}
	return b.String()
}

func (a *App) renderHeader() string {
	ws := a.cfg.WorkspaceName
	if ws == "" {
		ws = "no workspace"
	}
	left := a.styles.Title.Render(" fcards ") + a.styles.Dim.Render("· "+ws)
	right := a.styles.Dim.Render(a.status)
	if a.offline {
		right = a.styles.Warn.Render(fmt.Sprintf("offline · reviewing from cache · %d queued", a.queued))
	} else if a.loading {
		right = a.styles.Warn.Render("syncing…")
	}
	gap := a.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (a *App) renderFooter() string {
	var keys string
	switch a.screen {
	case screenDashboard:
		extra := ""
		if n := len(a.svc.TagSelection()); n > 0 {
			extra = fmt.Sprintf(" · filter: %d tag(s) active (f to edit)", n)
		}
		keys = "r review · f filter · b browse · s stats · w workspace · R sync · ? keys · q quit" + extra
	case screenReview:
		keys = "space reveal · 1-4 rate · s skip · esc back · ? keys"
	case screenBrowse:
		keys = "/ filter · enter detail · esc back · ? keys"
	case screenStats:
		keys = "esc back · R sync · ? keys"
	}
	return a.styles.StatusBar.Render(" " + keys)
}

func (a *App) renderHelp() string {
	lines := []string{
		"fcards keys",
		"",
		"  r        start a review session",
		"  b        browse & search cards",
		"  s        stats (streak, heatmap)",
		"  w        switch workspace",
		"  R        force sync with the cloud",
		"  ?        toggle this help",
		"  q/ctrl+c quit",
		"",
		"review",
		"  space    reveal answer / advance",
		"  1        again",
		"  2        hard",
		"  3        good",
		"  4        easy",
		"  s        skip card (defers to end of session)",
		"  esc      back to dashboard",
	}
	body := strings.Join(lines, "\n")
	return a.styles.Border.Render(body)
}

func (a *App) renderWorkspaceOverlay() string {
	var rows []string
	for i, w := range a.workspaceList {
		cursor := "  "
		name := w.Name
		if i == a.workspaceCur {
			cursor = "> "
			name = a.styles.Selected.Render(name)
		}
		marker := ""
		if w.ID() == a.cfg.WorkspaceID {
			marker = a.styles.Dim.Render(" (current)")
		}
		rows = append(rows, cursor+name+marker)
	}
	title := a.styles.Title.Render("switch workspace")
	return a.styles.Border.Render(title + "\n\n" + strings.Join(rows, "\n") + "\n\n  enter select · esc close")
}

// updateLogin routes to the login model.
func (a *App) updateLogin(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "q" {
		return a, tea.Quit
	}
	m, cmd := a.login.Update(msg)
	a.login = m
	if a.login.Done() {
		return a, func() tea.Msg { return loginSuccessMsg{} }
	}
	return a, cmd
}

func (a *App) updateDashboard(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "q":
			return a, tea.Quit
		case "r", "enter":
			if q := a.svc.Queue(timeNow()); len(q) > 0 {
				a.review.Begin(a.svc)
				a.screen = screenReview
				return a, imageKick()
			}
			a.status = "nothing due in this selection"
			return a, nil
		case "f":
			a.tagPicker.Rebuild(a.svc)
			a.tagPicker.filter.Focus()
			a.tagPicker.statusMsg = ""
			a.showTags = true
			return a, textinput.Blink
		case "b":
			a.screen = screenBrowse
			return a, a.browse.Focus()
		case "s":
			a.screen = screenStats
			return a, nil
		case "w":
			return a, a.openWorkspaceOverlay()
		}
	}
	return a, nil
}

func (a *App) updateReview(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" {
		a.screen = screenDashboard
		return a, a.refreshCmd()
	}
	m, cmd := a.review.Update(msg)
	a.review = m
	return a, cmd
}

func (a *App) updateBrowse(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "esc" && !a.browse.IsFiltering() {
		a.screen = screenDashboard
		return a, nil
	}
	m, cmd := a.browse.Update(msg)
	a.browse = m
	return a, cmd
}

func (a *App) updateStats(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "q", "esc":
			a.screen = screenDashboard
			return a, nil
		}
	}
	return a, nil
}
