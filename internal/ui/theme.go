// Package ui contains the fcards bubbletea interface.
package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme maps the current Omarchy theme palette onto fcards styles.
type Theme struct {
	Accent    lipgloss.Color
	AccentAlt lipgloss.Color
	Fg        lipgloss.Color
	Gray      lipgloss.Color
	Red       lipgloss.Color
	Green     lipgloss.Color
	Yellow    lipgloss.Color
	Blue      lipgloss.Color
	Magenta   lipgloss.Color
	Cyan      lipgloss.Color
}

// LoadTheme reads the active Omarchy theme's colors.toml accent and falls
// back to ANSI colors that follow the terminal palette automatically.
func LoadTheme() Theme {
	t := Theme{
		Accent:  lipgloss.Color("6"), // terminal cyan as fallback
		Fg:      lipgloss.Color("7"),
		Gray:    lipgloss.Color("8"),
		Red:     lipgloss.Color("1"),
		Green:   lipgloss.Color("2"),
		Yellow:  lipgloss.Color("3"),
		Blue:    lipgloss.Color("4"),
		Magenta: lipgloss.Color("5"),
		Cyan:    lipgloss.Color("6"),
	}
	if accent := omarchyAccent(); accent != "" {
		t.Accent = lipgloss.Color(accent)
	}
	// A softer secondary highlight from the palette.
	t.AccentAlt = t.Green
	return t
}

func omarchyAccent() string {
	name := omarchyThemeName()
	if name == "" {
		return ""
	}
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, dir := range []string{
		filepath.Join(home, ".config", "omarchy", "themes", slug),
		filepath.Join("/usr/share/omarchy/themes", slug),
	} {
		data, err := os.ReadFile(filepath.Join(dir, "colors.toml"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "accent") {
				if _, value, ok := strings.Cut(line, "="); ok {
					value = strings.TrimSpace(value)
					value = strings.Trim(value, "\"'")
					if strings.HasPrefix(value, "#") {
						return value
					}
				}
			}
		}
	}
	return ""
}

func omarchyThemeName() string {
	out, err := exec.Command("omarchy", "theme", "current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// --- shared styles ---

type Styles struct {
	Title, Header, Border, Dim, Error, Success, Warn, Selected, Key lipgloss.Style
	CardBox, StatusBar                                              lipgloss.Style
}

func NewStyles(t Theme) Styles {
	return Styles{
		Title: lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		Header: lipgloss.NewStyle().Bold(true).Foreground(t.Accent).
			MarginBottom(0).Padding(0, 1),
		Border:   lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.Gray),
		Dim:      lipgloss.NewStyle().Foreground(t.Gray),
		Error:    lipgloss.NewStyle().Foreground(t.Red).Bold(true),
		Success:  lipgloss.NewStyle().Foreground(t.Green),
		Warn:     lipgloss.NewStyle().Foreground(t.Yellow),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		Key:      lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		CardBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Accent).
			Padding(1, 2),
		StatusBar: lipgloss.NewStyle().Foreground(t.Gray).Padding(0, 1),
	}
}
