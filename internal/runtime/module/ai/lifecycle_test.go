package ai

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	llm "github.com/gtkit/go-llm-provider/v2/provider"

	"github.com/gtkit/gin-api/internal/platform/config"
)

type trackedTransport struct{ closed int }

func (*trackedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected network call")
}
func (t *trackedTransport) CloseIdleConnections() { t.closed++ }

func TestModuleClientCleanup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		providers map[string]config.AIProviderConfig
		wantErr   bool
	}{
		{"success", map[string]config.AIProviderConfig{"deepseek": {APIKey: "test"}}, false},
		{"partial construction failure", map[string]config.AIProviderConfig{"deepseek": {APIKey: "test"}, "zzunknown": {APIKey: "test"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var transports []*trackedTransport
			client, err := buildClientWithFactory(enabledConfig(tc.providers, "deepseek"), func() *http.Client {
				transport := &trackedTransport{}
				transports = append(transports, transport)
				return &http.Client{Transport: transport}
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("build = %v", err)
			}
			if !tc.wantErr {
				var wg sync.WaitGroup
				for range 10 {
					wg.Go(func() {
						if err := client.Close(); err != nil {
							t.Error(err)
						}
					})
				}
				wg.Wait()
			}
			if len(transports) == 0 {
				t.Fatal("no client was constructed")
			}
			for _, transport := range transports {
				if transport.closed != 1 {
					t.Fatalf("close calls = %d", transport.closed)
				}
			}
		})
	}
}

type failingProvider struct{ calls int }

func (*failingProvider) Name() llm.ProviderName { return llm.ProviderDeepSeek }
func (p *failingProvider) Chat(context.Context, *llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls++
	return nil, &llm.ProviderError{Code: llm.ErrorCodeRateLimit, Retryable: true, Message: "retryable failure"}
}
func (p *failingProvider) ChatStream(ctx context.Context, req *llm.ChatRequest) (*llm.StreamReader, error) {
	_, err := p.Chat(ctx, req)
	return nil, err
}

func TestAIBreakerCountsRequests(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "chat", true: "stream"}[stream], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				base := &failingProvider{}
				provider, err := harden(base, "deepseek", config.AIModuleConfig{RetryAttempts: 3, Breaker: config.AIBreakerConfig{ConsecutiveFailures: 2, OpenTimeout: time.Minute}})
				if err != nil {
					t.Fatal(err)
				}
				for request := range 3 {
					if stream {
						_, err = provider.ChatStream(t.Context(), &llm.ChatRequest{})
					} else {
						_, err = provider.Chat(t.Context(), &llm.ChatRequest{})
					}
					if err == nil {
						t.Fatal("expected upstream or breaker error")
					}
					want := min(request+1, 2) * 3
					if base.calls != want {
						t.Fatalf("request %d: attempts=%d, want %d", request+1, base.calls, want)
					}
				}
			})
		})
	}
}
