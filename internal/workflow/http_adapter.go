package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HTTPAdapter is the integration point for a self-hosted durable orchestrator
// exposing the HarnessMesh workflow contract. It is useful for Restate-like
// HTTP deployments and test doubles; Temporal deployments should implement
// the same DurableWorkflow interface with their native SDK.
type HTTPAdapter struct {
	Endpoint string
	Token    string
	Client   *http.Client
}

func (a HTTPAdapter) Submit(ctx context.Context, req WorkflowRequest) (string, error) {
	var out WorkflowExecution
	if err := a.call(ctx, http.MethodPost, "/workflows", req, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("workflow provider returned no id")
	}
	return out.ID, nil
}
func (a HTTPAdapter) Get(ctx context.Context, id string) (WorkflowExecution, error) {
	var out WorkflowExecution
	err := a.call(ctx, http.MethodGet, "/workflows/"+id, nil, &out)
	return out, err
}
func (a HTTPAdapter) Cancel(ctx context.Context, id, reason string) error {
	return a.call(ctx, http.MethodPost, "/workflows/"+id+"/cancel", map[string]string{"reason": reason}, nil)
}

func (a HTTPAdapter) call(ctx context.Context, method, path string, payload any, result any) error {
	if strings.TrimSpace(a.Endpoint) == "" {
		return errors.New("workflow endpoint is required")
	}
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.Endpoint, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("workflow provider returned %s", resp.Status)
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}
