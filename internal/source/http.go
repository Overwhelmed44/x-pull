// Package source retrieves complete desired user snapshots over HTTP.
package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/x-pull/x-pull/internal/config"
	"github.com/x-pull/x-pull/internal/user"
)

type Client struct {
	http     *http.Client
	maxBytes int64
}

func New(timeout time.Duration, maxBytes int64) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	return &Client{http: &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, maxBytes: maxBytes}
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Fetch(ctx context.Context, source config.Source) ([]user.User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create HTTP request")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range source.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// net/http errors may contain URLs with secret query parameters.
		return nil, fmt.Errorf("HTTP request failed (network, TLS, or timeout)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP source returned status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read HTTP response")
	}
	if int64(len(data)) > c.maxBytes {
		return nil, fmt.Errorf("HTTP response exceeds configured size limit")
	}
	var snapshot struct {
		Users *[]user.User `json:"users"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("HTTP source returned invalid snapshot JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("HTTP source must return exactly one JSON object")
	}
	if snapshot.Users == nil {
		return nil, fmt.Errorf("HTTP source must contain a non-null users array")
	}
	return *snapshot.Users, nil
}
