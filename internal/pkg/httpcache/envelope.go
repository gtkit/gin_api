package httpcache

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/goerr"
	"github.com/gtkit/logger/v2"
)

const (
	envelopeModeKey  = "httpcache.unified_json"
	envelopeLimitKey = "httpcache.envelope_limit"
)

// ErrInvalidEnvelope rejects incompatible routes before committing their body.
var ErrInvalidEnvelope = errors.New("httpcache: unified cache requires one ordinary JSON response envelope")

// UnifiedJSONCache adapts a cache middleware for routes using response.WriteJSON
// (including Ok/Err). Each request gets its own top-level request_id. Generic
// caches keep their original payloads and validators. Streaming, signed and
// compressed responses must use the generic cache or bypass caching.
func UnifiedJSONCache(cached gin.HandlerFunc) gin.HandlerFunc {
	if cached == nil {
		panic("httpcache: cache middleware is required")
	}
	return func(c *gin.Context) {
		original, request := c.Writer, c.Request
		c.Set(envelopeModeKey, true)
		// Per-request bytes cannot reuse a validator for another request's envelope.
		clone := request.Clone(request.Context())
		deleteHeaders(clone.Header, "If-None-Match", "If-Modified-Since")
		c.Request = clone
		writer := &envelopeWriter{ResponseWriter: original, ctx: c}
		c.Writer = writer
		defer func() { c.Writer = original; c.Request = request; c.Set(envelopeModeKey, false) }()
		cached(c)
		c.Writer = original
		if writer.err == nil {
			return
		}
		c.Error(writer.err).Type = gin.ErrorTypePrivate
		if original.Written() {
			resp.ObserveError(c, writer.err)
			return
		}
		stripEnvelopeHeaders(original.Header())
		deleteHeaders(original.Header(), "Content-Type", "Content-Encoding", "Signature", "Signature-Input")
		resp.Err(c, writer.err)
	}
}

type envelopeWriter struct {
	gin.ResponseWriter
	ctx   *gin.Context
	err   error
	wrote bool
}

// Unwrap keeps ResponseController deadlines connected to the underlying socket.
func (w *envelopeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *envelopeWriter) fail(err error) {
	if w.err == nil {
		w.err = goerr.Wrap(err, "write unified cached response")
		w.Header().Set("Cache-Control", "no-store")
	}
}

func (w *envelopeWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	status := w.Status()
	if w.ctx.Request.Method == http.MethodHead || status == http.StatusNoContent || status == http.StatusNotModified {
		w.WriteHeaderNow()
		return len(data), nil
	}
	mediaType, _, mediaErr := mime.ParseMediaType(w.Header().Get("Content-Type"))
	if mediaErr != nil {
		w.fail(errors.Join(ErrInvalidEnvelope, mediaErr))
		return 0, w.err
	}
	if w.wrote || mediaType != "application/json" ||
		w.Header().Get("Content-Encoding") != "" || w.Header().Get("Signature") != "" || w.Header().Get("Signature-Input") != "" {
		w.fail(ErrInvalidEnvelope)
		return 0, w.err
	}
	limit := w.ctx.GetInt64(envelopeLimitKey)
	if limit <= 0 {
		limit = (config.CacheConfig{}).EffectiveMaxBodyBytes()
	}
	output := data
	if int64(len(data)) > limit {
		// This is a fresh, current-ID helper response. The cache's own size gate
		// rejects it; do not allocate another oversized copy for validation/rewrite.
		if !resp.IsEnvelopeWrite(w.ctx) {
			w.fail(ErrInvalidEnvelope)
			return 0, w.err
		}
	} else {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			w.fail(errors.Join(ErrInvalidEnvelope, err))
			return 0, w.err
		}
		if len(fields) != 4 || fields["code"] == nil || fields["msg"] == nil || fields["data"] == nil || fields["request_id"] == nil ||
			string(fields["code"]) == "null" || string(fields["msg"]) == "null" || string(fields["request_id"]) == "null" {
			w.fail(ErrInvalidEnvelope)
			return 0, w.err
		}
		var envelope struct {
			Code      goerr.Code      `json:"code"`
			Message   string          `json:"msg"`
			Data      json.RawMessage `json:"data"`
			RequestID string          `json:"request_id"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			w.fail(errors.Join(ErrInvalidEnvelope, err))
			return 0, w.err
		}
		envelope.RequestID = logger.RequestIDFromContext(w.ctx.Request.Context())
		encoded, err := json.Marshal(envelope)
		if err != nil {
			w.fail(fmt.Errorf("encode response envelope: %w", err))
			return 0, w.err
		}
		output = encoded
	}
	stripEnvelopeHeaders(w.Header())
	id := logger.RequestIDFromContext(w.ctx.Request.Context())
	if id != "" {
		w.Header().Set("X-Request-ID", id)
	} else {
		w.Header().Del("X-Request-ID")
	}
	w.wrote = true
	n, err := w.ResponseWriter.Write(output)
	if err == nil && n != len(output) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.fail(err)
		return 0, w.err
	}
	return len(data), nil
}

func (w *envelopeWriter) WriteString(value string) (int, error) { return w.Write([]byte(value)) }
func (w *envelopeWriter) WriteHeaderNow() {
	status := w.Status()
	if w.ctx.Request.Method != http.MethodHead && status != http.StatusNoContent && status != http.StatusNotModified {
		w.fail(ErrInvalidEnvelope)
		return
	}
	stripEnvelopeHeaders(w.Header())
	w.ResponseWriter.WriteHeaderNow()
}
func (w *envelopeWriter) Flush() { w.fail(ErrInvalidEnvelope) }
func (w *envelopeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.fail(ErrInvalidEnvelope)
	return nil, nil, w.err
}

func stripEnvelopeHeaders(header http.Header) {
	deleteHeaders(header, "Content-Length", "ETag", "Last-Modified", "Content-MD5", "Digest", "Content-Digest", "Repr-Digest")
}
func deleteHeaders(header http.Header, names ...string) {
	for key := range header {
		for _, name := range names {
			if strings.EqualFold(key, name) {
				delete(header, key)
				break
			}
		}
	}
}
