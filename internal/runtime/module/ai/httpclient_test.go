package ai

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
	llm "github.com/gtkit/go-llm-provider/v2/provider"
)

type responseProbeBody struct {
	*strings.Reader
	closed   int
	closeErr error
}

func (b *responseProbeBody) Close() error { b.closed++; return b.closeErr }

type trailingErrorBody struct {
	io.ReadCloser
	err error
}

func (b trailingErrorBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		return n, b.err
	}
	return n, err
}

func TestResponseOverflowPreservesReadFailure(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{io.ErrUnexpectedEOF, io.EOF} {
		body := &limitedResponseBody{ReadCloser: trailingErrorBody{ReadCloser: io.NopCloser(strings.NewReader("abcdef")), err: cause}, remaining: 5}
		_, err := io.ReadAll(body)
		if !errors.Is(err, errResponseTooLarge) || errors.Is(err, io.EOF) {
			t.Fatalf("overflow was lost or became EOF: %v", err)
		}
		if cause != io.EOF && !errors.Is(err, cause) {
			t.Fatalf("read cause was lost: %v", err)
		}
		if _, repeated := body.Read(make([]byte, 1)); repeated != err {
			t.Fatalf("read failure was not stable: %v", repeated)
		}
		if err := body.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResponseBodyLimitDistinguishesEOFAndOverflow(t *testing.T) {
	t.Parallel()
	for _, data := range []string{"abcd", "abcde", "abcdef"} {
		t.Run(data, func(t *testing.T) {
			closeErr := errors.New("close failed")
			source := &responseProbeBody{Reader: strings.NewReader(data), closeErr: closeErr}
			body := &limitedResponseBody{ReadCloser: source, remaining: 5}
			out, err := io.ReadAll(body)
			if len(data) <= 5 {
				if err != nil || string(out) != data {
					t.Fatalf("valid body changed: %q %v", out, err)
				}
			} else {
				if !errors.Is(err, errResponseTooLarge) || string(out) != data[:5] {
					t.Fatalf("overflow became success or EOF: %q %v", out, err)
				}
				if _, err := body.Read(make([]byte, 1)); !errors.Is(err, errResponseTooLarge) {
					t.Fatalf("overflow was not sticky: %v", err)
				}
			}
			if err := body.Close(); !errors.Is(err, closeErr) || source.closed != 1 {
				t.Fatalf("underlying Close lost: calls=%d error=%v", source.closed, err)
			}
		})
	}
}

func TestProviderResponseBudgetPrecedesSDKBuffering(t *testing.T) {
	t.Parallel()
	for _, name := range []llm.ProviderName{llm.ProviderDeepSeek, llm.ProviderAnthropic, llm.ProviderGemini} {
		for _, mode := range []string{"chat", "stream", "error response"} {
			t.Run(string(name)+"/"+mode, func(t *testing.T) {
				data, contentType, status := `{"padding":"`+strings.Repeat("x", 128)+`"}`, "application/json", http.StatusOK
				if mode == "stream" {
					data, contentType = "data: "+strings.Repeat("x", 128), "text/event-stream"
				}
				if mode == "error response" {
					status = http.StatusInternalServerError
				}
				body := &responseProbeBody{Reader: strings.NewReader(data)}
				client := &http.Client{Transport: protocolProbeTransport(func(req *http.Request) (*http.Response, error) {
					if err := req.Body.Close(); err != nil {
						return nil, err
					}
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}},
						Body: body, ContentLength: -1, Request: req}, nil
				})}
				provider, err := newProvider(name, "test", "https://model.invalid", "test-model",
					&limitedHTTPClient{client: client, maxBytes: 32})
				if err != nil {
					t.Fatal(err)
				}
				provider, err = harden(provider, string(name), config.AIModuleConfig{RetryAttempts: 3})
				if err != nil {
					t.Fatal(err)
				}
				request := &llm.ChatRequest{Messages: []llm.Message{llm.UserText("hello")}, MaxTokens: 16}
				if mode == "stream" {
					stream, openErr := provider.ChatStream(t.Context(), request)
					err = openErr
					if stream != nil {
						_, err = stream.Recv()
						err = errors.Join(err, stream.Close())
					}
				} else {
					_, err = provider.Chat(t.Context(), request)
				}
				if !errors.Is(err, errResponseTooLarge) {
					t.Fatalf("response limit cause lost across SDK: %v", err)
				}
				if body.closed != 1 {
					t.Fatalf("body Close calls = %d, want 1; a deterministic limit must not retry", body.closed)
				}
			})
		}
	}
}
