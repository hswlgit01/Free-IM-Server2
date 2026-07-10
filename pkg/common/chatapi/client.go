// Package chatapi provides a small, hardened client for Free-IM-Chat internal
// endpoints. It centralizes authentication, redirect handling, response size
// limits, and the common API envelope so individual services do not reimplement
// subtly different HTTP behavior.
package chatapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBaseURL       = "http://127.0.0.1:10008"
	InternalSecretHeader = "X-Internal-Secret"
	DefaultTimeout       = 2 * time.Second
	MaxResponseSize      = 64 << 10
)

type Client struct {
	baseURL        string
	internalSecret string
	httpClient     *http.Client
}

type Response struct {
	ErrCode int             `json:"errCode"`
	ErrMsg  string          `json:"errMsg"`
	ErrDlt  string          `json:"errDlt"`
	Data    json.RawMessage `json:"data"`
}

func New(chatAPIURL, internalSecret string) *Client {
	if strings.TrimSpace(chatAPIURL) == "" {
		chatAPIURL = DefaultBaseURL
	}
	return &Client{
		baseURL:        strings.TrimRight(chatAPIURL, "/"),
		internalSecret: internalSecret,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *Client) Do(
	ctx context.Context,
	method,
	path string,
	query url.Values,
	payload any,
	operationID string,
) (*Response, error) {
	if strings.TrimSpace(c.internalSecret) == "" {
		return nil, fmt.Errorf("Free-IM-Chat internal secret is not configured")
	}

	endpoint, err := url.Parse(c.baseURL + "/" + strings.TrimLeft(path, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse Free-IM-Chat endpoint: %w", err)
	}
	if query != nil {
		values := endpoint.Query()
		for key, items := range query {
			for _, item := range items {
				values.Add(key, item)
			}
		}
		endpoint.RawQuery = values.Encode()
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal Free-IM-Chat request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create Free-IM-Chat request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if operationID != "" {
		req.Header.Set("operationID", operationID)
	}
	req.Header.Set(InternalSecretHeader, c.internalSecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call Free-IM-Chat endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Free-IM-Chat endpoint returned HTTP %d", resp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Free-IM-Chat response: %w", err)
	}
	if len(bodyBytes) > MaxResponseSize {
		return nil, fmt.Errorf("Free-IM-Chat response exceeds %d bytes", MaxResponseSize)
	}

	var result Response
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("decode Free-IM-Chat response: %w", err)
	}
	return &result, nil
}

func (r *Response) DecodeData(out any) error {
	if len(r.Data) == 0 || string(r.Data) == "null" {
		return fmt.Errorf("Free-IM-Chat response returned no data")
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		return fmt.Errorf("decode Free-IM-Chat response data: %w", err)
	}
	return nil
}
