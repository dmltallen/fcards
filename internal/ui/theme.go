// Package ui contains the fcards bubbletea interface.
package ui

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme maps the current Omarchy theme palette onto fcards styles.
type Theme struct {
	Accent    lipgloss.Color
	AccentAlt lipgloss.Color
	Fg        lipgloss.Color
	Gray      lipgloss.Color
	Dim       lipgloss.Color // subdued text, contrast-checked against bg
	Subtle    lipgloss.Color // decorative (borders, separators), lower target
	Red       lipgloss.Color
	Green     lipgloss.Color
	Yellow    lipgloss.Color
	Blue      lipgloss.Color
	Magenta   lipgloss.Color
	Cyan      lipgloss.Color
}

// LoadTheme reads the active Omarchy theme's colors.toml and derives subdued
// colors with guaranteed contrast. Themes vary wildly in what they expose —
// some ship color0-15, others (everforest) only semantic keys like `muted`,
// which can be nearly invisible. So instead of trusting any single key, we
// blend foreground toward background until a WCAG contrast target is met.
func LoadTheme() Theme {
	t := Theme{
		Accent:  lipgloss.Color("6"), // terminal cyan as fallback
		Fg:      lipgloss.Color("7"),
		Gray:    lipgloss.Color("8"),
		Dim:     lipgloss.Color("8"),
		Subtle:  lipgloss.Color("8"),
		Red:     lipgloss.Color("1"),
		Green:   lipgloss.Color("2"),
		Yellow:  lipgloss.Color("3"),
		Blue:    lipgloss.Color("4"),
		Magenta: lipgloss.Color("5"),
		Cyan:    lipgloss.Color("6"),
	}

	vals := omarchyColors()
	if accent := vals["accent"]; strings.HasPrefix(accent, "#") {
		t.Accent = lipgloss.Color(accent)
	}
	t.AccentAlt = t.Green

	if bg, fg := vals["background"], vals["foreground"]; bg != "" && fg != "" {
		// Subdued text must stay readable (WCAG AA for the small labels it
		// carries); decorative lines only need to be perceivable.
		t.Dim = lipgloss.Color(deriveColor(bg, fg, 4.5, vals["muted"], vals["color8"], vals["color7"]))
		t.Subtle = lipgloss.Color(deriveColor(bg, fg, 3.0, vals["muted"], vals["color8"]))
		t.Gray = t.Subtle
	}
	return t
}

// omarchyColors loads the active theme's colors.toml as key->hex map,
// checking both user and system theme directories.
func omarchyColors() map[string]string {
	out := map[string]string{}
	name := omarchyThemeName()
	if name == "" {
		return out
	}
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	home, err := os.UserHomeDir()
	if err != nil {
		return out
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
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			value = strings.Trim(strings.TrimSpace(value), "\"'")
			if key != "" && strings.HasPrefix(value, "#") && len(value) >= 7 {
				if _, exists := out[key]; !exists {
					out[key] = value[:7]
				}
			}
		}
		break // first directory that has the theme wins
	}
	return out
}

func omarchyThemeName() string {
	out, err := exec.Command("omarchy", "theme", "current").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// --- contrast-derived subdued colors ---

func hexRGB(hex string) (r, g, b float64, ok bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff), true
}

func toHex(r, g, b float64) string {
	clamp := func(v float64) string {
		n := math.Round(math.Min(255, math.Max(0, v)))
		return fmt.Sprintf("%02x", int(n))
	}
	return "#" + clamp(r) + clamp(g) + clamp(b)
}

func relativeLuminance(hex string) float64 {
	r, g, b, ok := hexRGB(hex)
	if !ok {
		return 0
	}
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func contrastRatio(a, b string) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	if lo < 0 {
		return 1
	}
	return (hi + 0.05) / (lo + 0.05)
}

// blend mixes fg into bg by fgShare (0..1) per channel.
func blend(bg, fg string, fgShare float64) string {
	br, bgc, bb, ok1 := hexRGB(bg)
	fr, fgc, fb, ok2 := hexRGB(fg)
	if !ok1 || !ok2 {
		return fg
	}
	return toHex(fr*fgShare+br*(1-fgShare), fgc*fgShare+bgc*(1-fgShare), fb*fgShare+bb*(1-fgShare))
}

// deriveColor returns a subdued foreground: the most subdued option that still
// reaches the WCAG contrast target against bg. Theme-provided candidates are
// trusted first; otherwise the foreground is blended toward the background.
func deriveColor(bg, fg string, target float64, candidates ...string) string {
	for _, c := range candidates {
		if strings.HasPrefix(c, "#") && contrastRatio(c, bg) >= target {
			return c
		}
	}
	for share := 0.45; share <= 0.95; share += 0.05 {
		mix := blend(bg, fg, share)
		if contrastRatio(mix, bg) >= target {
			return mix
		}
	}
	return blend(bg, fg, 0.95) // extremely low-contrast theme: near-full fg
}

// --- shared styles ---

type Styles struct {
	Title, Header, Border, Dim, Error, Success, Warn, Selected, Key lipgloss.Style
	CardBox, StatusBar                                              lipgloss.Style
}

func NewStyles(t Theme) Styles {
	return Styles{
		Title:    lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		Header:   lipgloss.NewStyle().Bold(true).Foreground(t.Accent).MarginBottom(0).Padding(0, 1),
		Border:   lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.Subtle),
		Dim:      lipgloss.NewStyle().Foreground(t.Dim),
		Error:    lipgloss.NewStyle().Foreground(t.Red).Bold(true),
		Success:  lipgloss.NewStyle().Foreground(t.Green),
		Warn:     lipgloss.NewStyle().Foreground(t.Yellow),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		Key:      lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		CardBox: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Subtle).
			Padding(1, 2),
		StatusBar: lipgloss.NewStyle().Foreground(t.Dim).Padding(0, 1),
	}
}
