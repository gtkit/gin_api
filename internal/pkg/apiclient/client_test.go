package apiclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRequestAndBusinessFailure(t *testing.T) {
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/logout" || r.Header.Get("Authorization") != "Bearer access" {
			t.Error("incorrect client request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":19,"msg":"login required","data":[],"request_id":"request-1"}`)
	}))
	client, err := New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Logout(t.Context(), "access", LogoutRequest{})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.StatusCode != 200 || apiErr.Code != 19 || apiErr.RequestID != "request-1" {
		t.Fatalf("business error lost: %v", err)
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error { b.closed = true; return nil }

func TestClientResponseBoundsAndClose(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		fail       bool
	}{
		{"success", `{"status":"ok"}`, 200, false},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), 200, true},
		{"invalid", `{`, 200, true},
		{"http error", `{"code":50001,"msg":"unavailable","data":[],"request_id":"r"}`, 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(tc.body)}
			client, err := New("https://api.example.test", &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: body, Header: make(http.Header)}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Health(t.Context())
			if (err != nil) != tc.fail || !body.closed {
				t.Fatalf("err=%v closed=%v", err, body.closed)
			}
		})
	}
}

func TestClientCancellationAndRedirect(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	client, err := New("https://api.example.test", &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Health(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	calls := 0
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Location", "/credentials-must-not-follow")
		w.WriteHeader(307)
	}))
	client, err = New(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Logout(t.Context(), "access", LogoutRequest{}); err == nil || calls != 1 {
		t.Fatalf("redirect followed: %v calls=%d", err, calls)
	}
}
