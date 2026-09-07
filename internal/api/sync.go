// Sync API client methods: the offline-first contract used by the official
// mobile clients (bootstrap, hot-change pull, operation push, review history).
// Authenticated with the same long-lived ApiKey; responses are raw JSON
// (errors come back as {"ok":false,"error":{...}}).
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SyncEntry is one change/entry from bootstrap or pull.
type SyncEntry struct {
	ChangeID   int64           `json:"changeId,omitempty"`
	EntityType string          `json:"entityType"`
	EntityID   string          `json:"entityId"`
	Action     string          `json:"action"`
	Payload    json.RawMessage `json:"payload"`
}

// BootstrapPullResult is the response of sync/bootstrap mode=pull.
type BootstrapPullResult struct {
	Mode                 string      `json:"mode"`
	Entries              []SyncEntry `json:"entries"`
	NextCursor           *string     `json:"nextCursor"`
	HasMore              bool        `json:"hasMore"`
	BootstrapHotChangeID int64       `json:"bootstrapHotChangeId"`
	RemoteIsEmpty        bool        `json:"remoteIsEmpty"`
}

// PullResult is the response of sync/pull.
type PullResult struct {
	Changes         []SyncEntry `json:"changes"`
	NextHotChangeID int64       `json:"nextHotChangeId"`
	HasMore         bool        `json:"hasMore"`
}

// Operation is one sync push operation (card upsert or review_event append).
type Operation struct {
	OperationID     string          `json:"operationId"`
	EntityID        string          `json:"entityId"`
	ClientUpdatedAt string          `json:"clientUpdatedAt"`
	EntityType      string          `json:"entityType"` // "card" | "review_event" | ...
	Action          string          `json:"action"`     // "upsert" | "append"
	Payload         json.RawMessage `json:"payload"`
}

// OpResult is the per-operation outcome of a push.
type OpResult struct {
	OperationID string  `json:"operationId"`
	EntityType  string  `json:"entityType"`
	EntityID    string  `json:"entityId"`
	Status      string  `json:"status"` // applied | ignored | duplicate | rejected
	Error       *string `json:"error"`
}

type pushResult struct {
	Operations []OpResult `json:"operations"`
}

// ReviewHistoryEvent is one row from review-history/pull.
type ReviewHistoryEvent struct {
	ReviewEventID    string `json:"reviewEventId"`
	WorkspaceID      string `json:"workspaceId"`
	CardID           string `json:"cardId"`
	ReplicaID        string `json:"replicaId"`
	ClientEventID    string `json:"clientEventId"`
	Rating           int    `json:"rating"`
	ReviewedAtClient string `json:"reviewedAtClient"`
	ReviewedAtServer string `json:"reviewedAtServer"`
	ReviewedTimeZone string `json:"reviewedTimeZone,omitempty"`
}

type reviewHistoryResult struct {
	ReviewEvents         []ReviewHistoryEvent `json:"reviewEvents"`
	NextReviewSequenceID int64                `json:"nextReviewSequenceId"`
	HasMore              bool                 `json:"hasMore"`
}

// syncDo performs a sync request (raw JSON response) against the v1 API.
func (c *Client) syncDo(method, path string, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.APIBase+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "ApiKey "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var failure struct {
		OK    bool            `json:"ok"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &failure) == nil && failure.Error != nil && len(failure.Error) > 0 {
		apiErr := &APIError{Status: resp.StatusCode, Message: string(failure.Error)}
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
		if json.Unmarshal(failure.Error, &structured) == nil {
			apiErr.Code = structured.Code
			apiErr.Message = structured.Message
			for _, issue := range structured.Details.ValidationIssues {
				apiErr.Message += fmt.Sprintf(" [%s %s: %s]", issue.Path, issue.Code, issue.Message)
			}
		}
		return nil, apiErr
	}
	if resp.StatusCode >= 400 {
		return nil, &APIError{Status: resp.StatusCode, Message: truncate(string(data), 300)}
	}
	return json.RawMessage(data), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// BootstrapPull loads the full workspace snapshot (workspace_scheduler_settings,
// cards, decks) using the cursor loop.
func (c *Client) BootstrapPull(workspaceID, installationID string, cursor *string, limit int) (*BootstrapPullResult, error) {
	body := map[string]any{
		"mode":           "pull",
		"installationId": installationID,
		"platform":       "web",
		"cursor":         cursor,
		"limit":          limit,
	}
	raw, err := c.syncDo("POST", "/workspaces/"+workspaceID+"/sync/bootstrap", body)
	if err != nil {
		return nil, err
	}
	var out BootstrapPullResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Pull fetches hot-state deltas after the given change id.
func (c *Client) Pull(workspaceID, installationID string, afterHotChangeID int64, limit int) (*PullResult, error) {
	body := map[string]any{
		"installationId":   installationID,
		"platform":         "web",
		"afterHotChangeId": afterHotChangeID,
		"limit":            limit,
	}
	raw, err := c.syncDo("POST", "/workspaces/"+workspaceID+"/sync/pull", body)
	if err != nil {
		return nil, err
	}
	var out PullResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Push submits operations as one atomic batch.
func (c *Client) Push(workspaceID, installationID string, operations []Operation) ([]OpResult, error) {
	body := map[string]any{
		"installationId": installationID,
		"platform":       "web",
		"operations":     operations,
	}
	raw, err := c.syncDo("POST", "/workspaces/"+workspaceID+"/sync/push", body)
	if err != nil {
		return nil, err
	}
	var out pushResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Operations, nil
}

// MediaAssetDownload describes a short-lived signed URL for one media asset.
type MediaAssetDownload struct {
	MediaAsset struct {
		MediaAssetID string `json:"mediaAssetId"`
		MimeType     string `json:"mimeType"`
		SizeBytes    int    `json:"sizeBytes"`
		SHA256       string `json:"sha256"`
	} `json:"mediaAsset"`
	Download struct {
		Method    string `json:"method"`
		URL       string `json:"url"`
		ExpiresAt string `json:"expiresAt"`
	} `json:"download"`
}

// MediaDownloadURL fetches the signed download URL for a media asset.
func (c *Client) MediaDownloadURL(workspaceID, mediaAssetID string) (*MediaAssetDownload, error) {
	raw, err := c.syncDo("GET", "/workspaces/"+workspaceID+"/media-assets/"+mediaAssetID+"/download-url", nil)
	if err != nil {
		return nil, err
	}
	var out MediaAssetDownload
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FetchBytes downloads raw bytes from a signed URL (no auth header — the URL
// itself is the credential).
func (c *Client) FetchBytes(rawURL string) ([]byte, error) {
	resp, err := c.HTTP.Get(rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch failed: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// ReviewHistoryPull fetches append-only review history after a sequence id.
func (c *Client) ReviewHistoryPull(workspaceID, installationID string, afterReviewSequenceID int64, limit int) (*reviewHistoryResult, error) {
	body := map[string]any{
		"installationId":        installationID,
		"platform":              "web",
		"afterReviewSequenceId": afterReviewSequenceID,
		"limit":                 limit,
	}
	raw, err := c.syncDo("POST", "/workspaces/"+workspaceID+"/sync/review-history/pull", body)
	if err != nil {
		return nil, err
	}
	var out reviewHistoryResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
