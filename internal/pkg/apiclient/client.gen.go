// Code generated from docs/openapi.json by project client; DO NOT EDIT.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 1 << 20

type Client struct {
	baseURL string
	http    *http.Client
}

// New reuses the supplied transport. Requests are bounded to 10s when no client
// timeout is set. Redirects are returned as errors to avoid forwarding credentials.
func New(baseURL string, client *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("base URL must be HTTP(S), without credentials, query or fragment")
	}
	value := http.Client{Timeout: 10 * time.Second}
	if client != nil {
		value = *client
		if value.Timeout <= 0 {
			value.Timeout = 10 * time.Second
		}
	}
	value.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &value}, nil
}

type Envelope[T any] struct {
	Code      int64  `json:"code"`
	Message   string `json:"msg"`
	Data      T      `json:"data"`
	RequestID string `json:"request_id"`
}

// APIError preserves HTTP and business status without retaining response bodies or credentials.
type APIError struct {
	StatusCode int
	Code       int64
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API request failed: HTTP %d, code %d", e.StatusCode, e.Code)
}

func (c *Client) do(ctx context.Context, method, path string, input any, accessToken string, out any, wrapped bool) error {
	var payload []byte
	if input != nil {
		var err error
		payload, err = json.Marshal(input)
		if err != nil {
			return err
		}
		if len(payload) > 8192 {
			return errors.New("request body exceeds API limit")
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err := errors.Join(readErr, response.Body.Close()); err != nil {
		return err
	}
	if len(body) > maxResponseBytes {
		return errors.New("API response exceeds limit")
	}
	var envelope struct {
		Code      *int64 `json:"code"`
		Message   string `json:"msg"`
		RequestID string `json:"request_id"`
	}
	decodeErr := json.Unmarshal(body, &envelope)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &APIError{StatusCode: response.StatusCode}
		if decodeErr == nil {
			failure.Message, failure.RequestID = envelope.Message, envelope.RequestID
			if envelope.Code != nil {
				failure.Code = *envelope.Code
			}
		}
		return failure
	}
	if wrapped {
		if decodeErr != nil || envelope.Code == nil {
			return errors.New("invalid API response envelope")
		}
		if *envelope.Code != 0 {
			return &APIError{StatusCode: response.StatusCode, Code: *envelope.Code, Message: envelope.Message, RequestID: envelope.RequestID}
		}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode API response: %w", err)
	}
	return nil
}

type ChangePasswordRequest struct {
	NewPassword string `json:"new_password"`
	OldPassword string `json:"old_password"`
}

type DependencyCheck struct {
	Error  string `json:"error,omitempty"`
	Status string `json:"status"`
}

type Liveness struct {
	Status string `json:"status"`
}

type LoginRequest struct {
	Password string `json:"password"`
	Phone    string `json:"phone"`
}

type LogoutRequest struct {
	RefreshToken string `json:"refresh_token,omitempty"`
}

type Readiness struct {
	Checks   map[string]DependencyCheck `json:"checks,omitempty"`
	Degraded []string                   `json:"degraded,omitempty"`
	Failed   []string                   `json:"failed,omitempty"`
	Phase    string                     `json:"phase,omitempty"`
	Status   string                     `json:"status"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type RegisterRequest struct {
	Nickname string `json:"nickname,omitempty"`
	Password string `json:"password"`
	Phone    string `json:"phone"`
}

type Token struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

func (c *Client) Login(ctx context.Context, request LoginRequest) (Envelope[Token], error) {
	var result Envelope[Token]
	err := c.do(ctx, "POST", "/api/v1/auth/login", request, "", &result, true)
	return result, err
}

func (c *Client) Logout(ctx context.Context, accessToken string, request LogoutRequest) (Envelope[json.RawMessage], error) {
	var result Envelope[json.RawMessage]
	err := c.do(ctx, "POST", "/api/v1/auth/logout", request, accessToken, &result, true)
	return result, err
}

func (c *Client) LogoutAll(ctx context.Context, accessToken string) (Envelope[json.RawMessage], error) {
	var result Envelope[json.RawMessage]
	err := c.do(ctx, "POST", "/api/v1/auth/logout-all", nil, accessToken, &result, true)
	return result, err
}

func (c *Client) ChangePassword(ctx context.Context, accessToken string, request ChangePasswordRequest) (Envelope[json.RawMessage], error) {
	var result Envelope[json.RawMessage]
	err := c.do(ctx, "POST", "/api/v1/auth/password/change", request, accessToken, &result, true)
	return result, err
}

func (c *Client) Refresh(ctx context.Context, request RefreshRequest) (Envelope[Token], error) {
	var result Envelope[Token]
	err := c.do(ctx, "POST", "/api/v1/auth/refresh", request, "", &result, true)
	return result, err
}

func (c *Client) Register(ctx context.Context, request RegisterRequest) (Envelope[Token], error) {
	var result Envelope[Token]
	err := c.do(ctx, "POST", "/api/v1/auth/register", request, "", &result, true)
	return result, err
}

func (c *Client) Health(ctx context.Context) (Liveness, error) {
	var result Liveness
	err := c.do(ctx, "GET", "/health", nil, "", &result, false)
	return result, err
}

func (c *Client) Ready(ctx context.Context) (Readiness, error) {
	var result Readiness
	err := c.do(ctx, "GET", "/ready", nil, "", &result, false)
	return result, err
}
