// Package api implements the flashcards-open-source-app Agent API client:
// OTP bootstrap, long-lived API key auth, and the constrained SQL surface.
package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultAPIBase  = "https://api.flashcards-open-source-app.com/v1"
	DefaultAuthBase = "https://auth.flashcards-open-source-app.com"
)

// Client talks to the Agent API with a long-lived fca_ API key.
type Client struct {
	HTTP     *http.Client
	APIBase  string
	AuthBase string
	APIKey   string
}

func NewClient(apiKey string) *Client {
	return &Client{
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		APIBase:  DefaultAPIBase,
		AuthBase: DefaultAuthBase,
		APIKey:   apiKey,
	}
}

// APIError is a structured error from the service.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("api error %d (%s): %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("api error %d: %s", e.Status, e.Message)
}

func (c *Client) do(method, url string, body any, authed bool) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authed && c.APIKey != "" {
		req.Header.Set("Authorization", "ApiKey "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}

	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		if resp.StatusCode >= 400 {
			return nil, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(data))}
		}
		return nil, fmt.Errorf("unexpected response envelope: %w", err)
	}
	if !envelope.OK || resp.StatusCode >= 400 {
		apiErr := &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(envelope.Error))}
		var structured struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				ValidationIssues []struct {
					Path    string `json:"path"`
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"validationIssues"`
			} `json:"details"`
		}
		if json.Unmarshal(envelope.Error, &structured) == nil {
			apiErr.Code = structured.Code
			if structured.Message != "" {
				apiErr.Message = structured.Message
			}
			for _, issue := range structured.Details.ValidationIssues {
				apiErr.Message += fmt.Sprintf(" [%s %s: %s]", issue.Path, issue.Code, issue.Message)
			}
		}
		return nil, apiErr
	}
	return envelope.Data, nil
}

// --- discovery ---

type Discovery struct {
	Service struct {
		Name string `json:"name"`
	} `json:"service"`
	Auth struct {
		SendCodeURL   string `json:"sendCodeUrl"`
		VerifyCodeURL string `json:"verifyCodeUrl"`
	} `json:"authentication"`
	Surface struct {
		AccountURL    string `json:"accountUrl"`
		WorkspacesURL string `json:"workspacesUrl"`
		SQLQueryURL   string `json:"sqlQueryUrl"`
		SQLExecuteURL string `json:"sqlExecuteURL"`
	} `json:"surface"`
}

func (c *Client) Discover() (*Discovery, error) {
	raw, err := c.do(http.MethodGet, c.APIBase+"/", nil, false)
	if err != nil {
		return nil, err
	}
	var d Discovery
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// --- auth bootstrap ---

type SendCodeResult struct {
	OTPSessionToken string `json:"otpSessionToken"`
}

func (c *Client) SendCode(email string) (*SendCodeResult, error) {
	raw, err := c.do(http.MethodPost, c.AuthBase+"/api/agent/send-code",
		map[string]string{"email": email}, false)
	if err != nil {
		return nil, err
	}
	var r SendCodeResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

type VerifyCodeResult struct {
	APIKey string `json:"apiKey"`
}

func (c *Client) VerifyCode(code, otpSessionToken, label string) (*VerifyCodeResult, error) {
	raw, err := c.do(http.MethodPost, c.AuthBase+"/api/agent/verify-code", map[string]string{
		"code":            code,
		"otpSessionToken": otpSessionToken,
		"label":           label,
	}, false)
	if err != nil {
		return nil, err
	}
	var r VerifyCodeResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.APIKey == "" {
		return nil, errors.New("verify-code returned no API key")
	}
	return &r, nil
}

// --- account / workspaces ---

type Me struct {
	UserID            string `json:"userId"`
	Email             string `json:"email"`
	UserIDColumnSnake string `json:"user_id"`
}

func (c *Client) Me() (*Me, error) {
	raw, err := c.do(http.MethodGet, c.APIBase+"/agent/me", nil, true)
	if err != nil {
		return nil, err
	}
	var m Me
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

type Workspace struct {
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceIDCamel string `json:"workspaceId"`
	Name             string `json:"name"`
	Selected         bool   `json:"selected"`
	IsSelected       bool   `json:"isSelected"`
}

func (w Workspace) ID() string {
	if w.WorkspaceID != "" {
		return w.WorkspaceID
	}
	return w.WorkspaceIDCamel
}

func (w Workspace) SelectedNow() bool { return w.Selected || w.IsSelected }

func (c *Client) Workspaces() ([]Workspace, error) {
	raw, err := c.do(http.MethodGet, c.APIBase+"/agent/workspaces?limit=100", nil, true)
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Workspaces []Workspace `json:"workspaces"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Workspaces != nil {
		return wrapped.Workspaces, nil
	}
	var ws []Workspace
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, err
	}
	return ws, nil
}

func (c *Client) CreateWorkspace(name string) (*Workspace, error) {
	raw, err := c.do(http.MethodPost, c.APIBase+"/agent/workspaces", map[string]string{"name": name}, true)
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Workspace *Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Workspace != nil {
		return wrapped.Workspace, nil
	}
	var w Workspace
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

func (c *Client) SelectWorkspace(workspaceID string) error {
	_, err := c.do(http.MethodPost, c.APIBase+"/agent/workspaces/"+workspaceID+"/select", map[string]string{}, true)
	return err
}

// --- SQL surface ---

// SQLQuery runs a strictly read-only statement (SHOW TABLES, DESCRIBE, SELECT).
func (c *Client) SQLQuery(sql string) ([]map[string]any, error) {
	raw, err := c.do(http.MethodPost, c.APIBase+"/agent/sql/query", map[string]string{"sql": sql}, true)
	if err != nil {
		return nil, err
	}
	return decodeRows(raw)
}

// SQLExecute runs write statements as one atomic batch when the service
// accepts a statements array; otherwise it falls back to single-statement
// calls, which is NOT atomic. Callers should prefer a batch and verify.
func (c *Client) SQLExecute(statements []string) error {
	if len(statements) == 0 {
		return nil
	}
	_, err := c.do(http.MethodPost, c.APIBase+"/agent/sql/execute", map[string]any{"statements": statements}, true)
	if err == nil {
		return nil
	}
	// Fall back to one statement per call for services that only accept {"sql": ...}.
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 400 {
		for _, s := range statements {
			if _, err2 := c.do(http.MethodPost, c.APIBase+"/agent/sql/execute", map[string]string{"sql": s}, true); err2 != nil {
				return err2
			}
		}
		return nil
	}
	return err
}

func decodeRows(raw json.RawMessage) ([]map[string]any, error) {
	// Rows may arrive as an array directly or wrapped in {rows: [...]}.
	var direct []map[string]any
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}
	var wrapped struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("cannot decode sql rows: %w", err)
	}
	return wrapped.Rows, nil
}
