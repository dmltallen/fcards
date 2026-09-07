// Command fcards is a terminal flashcards client for the
// flashcards-open-source-app Agent API, built for Omarchy.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"

	"fcards/internal/api"
	"fcards/internal/store"
	"fcards/internal/sync"
	"fcards/internal/ui"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--help", "-h":
			fmt.Print(`fcards — flashcards in your terminal (Omarchy edition)

keys:
  r review · b browse · s stats · w workspace · R sync · ? keys · q quit
review: space reveal · 1 again · 2 hard · 3 good · 4 easy

data lives in your flashcards-open-source-app account (Agent API);
offline reviews are queued in ~/.local/share/fcards and flushed on launch.
`)
			return
		}
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	if cfg.ReplicaID == "" {
		cfg.ReplicaID = "fcards-" + uuid.NewString()[:8]
		_ = store.SaveConfig(cfg)
	}
	if cfg.InstallationID == "" {
		cfg.InstallationID = uuid.NewString()
		_ = store.SaveConfig(cfg)
	}

	client := api.NewClient(cfg.APIKey)
	var svc *sync.Service
	if cfg.APIKey != "" {
		svc = sync.New(client, cfg, cfg.ReplicaID, cfg.InstallationID)
		pickWorkspace(client, cfg, svc)
	}

	app := ui.NewApp(cfg, client, svc)
	program := tea.NewProgram(app, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "fcards:", err)
		os.Exit(1)
	}
}

// pickWorkspace selects the configured workspace (or the first available)
// so the SQL surface is scoped before the TUI starts.
func pickWorkspace(client *api.Client, cfg *store.Config, svc *sync.Service) {
	ws, err := client.Workspaces()
	if err != nil || len(ws) == 0 {
		return
	}
	if cfg.WorkspaceID != "" {
		for _, w := range ws {
			if w.ID() == cfg.WorkspaceID {
				_ = client.SelectWorkspace(cfg.WorkspaceID)
				return
			}
		}
	}
	// Prefer the service-marked selected workspace, else the first.
	for _, w := range ws {
		if w.Selected {
			cfg.WorkspaceID = w.ID()
			cfg.WorkspaceName = w.Name
			_ = store.SaveConfig(cfg)
			_ = client.SelectWorkspace(cfg.WorkspaceID)
			return
		}
	}
	cfg.WorkspaceID = ws[0].ID()
	cfg.WorkspaceName = ws[0].Name
	_ = store.SaveConfig(cfg)
	_ = client.SelectWorkspace(cfg.WorkspaceID)
}
