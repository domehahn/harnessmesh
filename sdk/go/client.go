package harnessmesh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Client struct { BaseURL, APIKey string; HTTP *http.Client }
func New(baseURL, apiKey string) *Client { return &Client{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: http.DefaultClient} }
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body *bytes.Reader
	if in == nil { body = bytes.NewReader(nil) } else { raw, err := json.Marshal(in); if err != nil { return err }; body = bytes.NewReader(raw) }
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1/dashboard"+path, body); if err != nil { return err }
	req.Header.Set("X-HarnessMesh-API-Key", c.APIKey); req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req); if err != nil { return err }; defer resp.Body.Close()
	if resp.StatusCode/100 != 2 { return fmt.Errorf("harnessmesh: %s", resp.Status) }
	if out != nil { return json.NewDecoder(resp.Body).Decode(out) }; return nil
}
func (c *Client) Summary(ctx context.Context, out any) error { return c.Do(ctx, http.MethodGet, "/summary", nil, out) }
func (c *Client) Skills(ctx context.Context, out any) error { return c.Do(ctx, http.MethodGet, "/skills", nil, out) }
func (c *Client) Marketplace(ctx context.Context, out any) error { return c.Do(ctx, http.MethodGet, "/marketplace", nil, out) }
