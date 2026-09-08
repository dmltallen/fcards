package ui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"fcards/internal/api"
	"fcards/internal/store"
)

var timeNow = time.Now

type codeSentMsg struct {
	token string
	err   error
}

type codeVerifiedMsg struct {
	err error
}

// LoginModel walks the email-OTP bootstrap flow.
type LoginModel struct {
	cfg    *store.Config
	client *api.Client
	theme  *Theme
	styles *Styles

	email textinput.Model
	code  textinput.Model
	step  int // 0 = email, 1 = code

	sessionToken string
	busy         bool
	err          string
	info         string
	finished     bool
}

func NewLogin(cfg *store.Config, client *api.Client, t *Theme, s *Styles) LoginModel {
	email := textinput.New()
	email.Placeholder = "you@example.com"
	email.Focus()
	code := textinput.New()
	code.Placeholder = "8-digit code from your email"
	return LoginModel{cfg: cfg, client: client, theme: t, styles: s, email: email, code: code}
}

// Done reports whether login finished (a loginSuccessMsg should follow).
func (m LoginModel) Done() bool { return m.finished }

func (m LoginModel) Update(msg tea.Msg) (LoginModel, tea.Cmd) {
	switch msg := msg.(type) {
	case codeSentMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.sessionToken = msg.token
		m.step = 1
		m.err = ""
		m.info = "code sent — check your email"
		m.code.Focus()
		m.email.Blur()
		return m, textinput.Blink

	case codeVerifiedMsg:
		m.busy = false
		if msg.err != nil {
			m.err = msg.err.Error()
			return m, nil
		}
		m.finished = true
		return m, nil
	}

	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.busy {
				return m, nil
			}
			if m.step == 0 && strings.TrimSpace(m.email.Value()) != "" {
				m.busy = true
				m.err = ""
				email := strings.TrimSpace(m.email.Value())
				return m, m.sendCodeCmd(email)
			}
			if m.step == 1 && strings.TrimSpace(m.code.Value()) != "" {
				m.busy = true
				m.err = ""
				return m, m.verifyCmd(strings.TrimSpace(m.code.Value()), m.sessionToken)
			}
		}
	}

	var cmd tea.Cmd
	if m.step == 0 {
		m.email, cmd = m.email.Update(msg)
	} else {
		m.code, cmd = m.code.Update(msg)
	}
	return m, cmd
}

func (m LoginModel) sendCodeCmd(email string) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		res, err := client.SendCode(email)
		if err != nil {
			return codeSentMsg{err: err}
		}
		return codeSentMsg{token: res.OTPSessionToken}
	}
}

func (m LoginModel) verifyCmd(code, token string) tea.Cmd {
	client := m.client
	cfg := m.cfg
	email := strings.TrimSpace(m.email.Value())
	return func() tea.Msg {
		res, err := client.VerifyCode(code, token, "fcards on omarchy")
		if err != nil {
			return codeVerifiedMsg{err: err}
		}
		cfg.Email = email
		cfg.APIKey = res.APIKey
		_ = store.SaveConfig(cfg)
		return codeVerifiedMsg{}
	}
}

func (m LoginModel) View(w, h int) string {
	var b strings.Builder
	b.WriteString("\n\n")
	logo := m.styles.Title.Render("fcards") + m.styles.Dim.Render(" · flashcards, in your terminal")
	b.WriteString("  " + logo + "\n\n")

	if m.step == 0 {
		b.WriteString("  sign in with your flashcards account (email code)\n\n")
		b.WriteString("  email  " + m.email.View() + "\n")
	} else {
		b.WriteString("  code sent to " + m.styles.Selected.Render(m.email.Value()) + "\n\n")
		b.WriteString("  code   " + m.code.View() + "\n")
	}
	if m.info != "" {
		b.WriteString("\n  " + m.styles.Success.Render(m.info))
	}
	if m.busy {
		b.WriteString("\n  " + m.styles.Warn.Render("working…"))
	}
	if m.err != "" {
		b.WriteString("\n  " + m.styles.Error.Render(m.err))
	}
	b.WriteString("\n\n  " + m.styles.Dim.Render("enter continue · ctrl+c quit"))
	return b.String()
}
