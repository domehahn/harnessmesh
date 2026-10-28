package integrations

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

// VoiceClient is a provider-neutral, tenant-bound HTTP adapter. Providers may
// expose transcription and synthesis endpoints with the documented JSON
// contract; HarnessMesh does not store audio or provider credentials.
type VoiceClient struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

type VoiceTranscript struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence,omitempty"`
}

func (c VoiceClient) Transcribe(ctx context.Context, tenant, format string, audio []byte) (VoiceTranscript, error) {
	if strings.TrimSpace(tenant) == "" || len(audio) == 0 {
		return VoiceTranscript{}, errors.New("tenant and audio are required")
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return VoiceTranscript{}, errors.New("voice endpoint is required")
	}
	return c.do(ctx, "/v1/audio/transcriptions", tenant, map[string]any{"format": format, "audio": audio})
}

func (c VoiceClient) Synthesize(ctx context.Context, tenant, text, voice string) ([]byte, error) {
	if strings.TrimSpace(tenant) == "" || strings.TrimSpace(text) == "" {
		return nil, errors.New("tenant and text are required")
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return nil, errors.New("voice endpoint is required")
	}
	reqBody, _ := json.Marshal(map[string]any{"text": text, "voice": voice})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoint, "/")+"/v1/audio/speech", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	c.headers(req, tenant)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("voice synthesis returned %s", resp.Status)
	}
	var result struct {
		Audio []byte `json:"audio"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Audio, nil
}

func (c VoiceClient) do(ctx context.Context, path, tenant string, payload map[string]any) (VoiceTranscript, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return VoiceTranscript{}, err
	}
	c.headers(req, tenant)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		return VoiceTranscript{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return VoiceTranscript{}, fmt.Errorf("voice transcription returned %s", resp.Status)
	}
	var result VoiceTranscript
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return VoiceTranscript{}, err
	}
	return result, nil
}

func (c VoiceClient) headers(req *http.Request, tenant string) {
	req.Header.Set("X-HarnessMesh-Tenant", tenant)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}
func (c VoiceClient) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}
