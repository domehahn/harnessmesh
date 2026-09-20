package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// EmbeddingProvider is intentionally small so deployments can use OpenAI,
// Ollama, a local model, or a vector database without changing Record.
type EmbeddingProvider interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

type OpenAIEmbeddingProvider struct {
	APIKey  string
	BaseURL string
	Model   string
	Client  *http.Client
}

func NewOpenAIEmbeddingProvider() *OpenAIEmbeddingProvider {
	base := os.Getenv("OPENAI_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	model := os.Getenv("HARNESSMESH_EMBEDDING_MODEL")
	if model == "" {
		model = "text-embedding-3-small"
	}
	return &OpenAIEmbeddingProvider{
		APIKey: os.Getenv("OPENAI_API_KEY"), BaseURL: strings.TrimRight(base, "/"), Model: model,
		Client: &http.Client{Timeout: 60 * time.Second},
	}
}

func (p *OpenAIEmbeddingProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if p == nil || p.APIKey == "" {
		return nil, fmt.Errorf("embedding provider is not configured")
	}
	body, err := json.Marshal(map[string]any{"model": p.Model, "input": inputs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding provider returned HTTP %d", resp.StatusCode)
	}
	var decoded struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Data) != len(inputs) {
		return nil, fmt.Errorf("embedding provider returned %d vectors for %d inputs", len(decoded.Data), len(inputs))
	}
	result := make([][]float32, len(inputs))
	for _, item := range decoded.Data {
		if item.Index < 0 || item.Index >= len(result) {
			return nil, fmt.Errorf("embedding provider returned invalid index %d", item.Index)
		}
		result[item.Index] = item.Embedding
	}
	return result, nil
}
