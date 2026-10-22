// Package log forwards project log events to the logger installed at startup.
// It owns no SDK, notifier, file or background queue.
package log

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// Level is the severity of a project log event.
type Level uint8

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// Stream selects the main or HTTP access output of a logging implementation.
type Stream uint8

const (
	MainStream Stream = iota
	AccessStream
)

// Field is a project-owned structured value. SDK conversion stays in a backend.
type Field struct {
	Key   string
	Value any
}

// LogEntry preserves severity, fields, output routing and explicit alert intent.
// A backend must copy Fields before retaining them beyond Log's return.
type LogEntry struct {
	Level   Level
	Message string
	Format  bool
	Args    []any
	Fields  []Field
	Stream  Stream
	Channel string
	Alert   bool
	// CallerSkip accounts for forwarding frames, excluding the backend itself.
	CallerSkip int
}

// Text materializes a printf event only when an implementation needs it.
func (entry LogEntry) Text() string {
	if entry.Format {
		return fmt.Sprintf(entry.Message, entry.Args...)
	}
	return entry.Message
}

// Logger is the read-only capability shared by logging consumers.
type Logger interface {
	Log(context.Context, LogEntry)
}

// LevelEnabler is an optional hint for lazy log preparation. Implementations
// query current levels and must permit concurrent calls; Log decides the final
// write. Alerts bypass ordinary level queries.
type LevelEnabler interface {
	Enabled(Level, Stream) bool
}

var (
	// ErrLoggerInstalled rejects replacing an active startup binding.
	ErrLoggerInstalled = errors.New("a project logger is already installed")
	// ErrInvalidLogger rejects an absent logging implementation.
	ErrInvalidLogger = errors.New("a project logger is required")
	installed        atomic.Pointer[binding]
	fallbackFailure  atomic.Pointer[fallbackWriteError]
)

type fallbackWriteError struct{ err error }

// Keep only the first outstanding failure, bounded independently of log volume.
// The process owner observes it through Drain; never recurse through this logger.
func writeDiagnostic(message string) {
	if _, err := fmt.Fprintln(os.Stderr, message); err != nil {
		fallbackFailure.CompareAndSwap(nil, &fallbackWriteError{err: fmt.Errorf("write fallback diagnostic: %w", err)})
	}
}

var diagnosticContext = context.Background()

type binding struct {
	logger Logger
	levels LevelEnabler
	drain  func(context.Context) error
}

// Install binds one selected implementation and its owner's optional drain.
// The returned release is idempotent and can only remove this exact binding.
// It never closes resources. The owner drains them after preventing new writes.
func Install(logger Logger, drain func(context.Context) error) (func(), error) {
	if logger == nil {
		return nil, ErrInvalidLogger
	}
	// An interface can hold a typed nil even when logger != nil. Validate it
	// only at installation; the log write path never uses reflection.
	value := reflect.ValueOf(logger)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, ErrInvalidLogger
		}
	}
	b := &binding{logger: logger, drain: drain}
	if levels, ok := logger.(LevelEnabler); ok {
		b.levels = levels
	}
	if !installed.CompareAndSwap(nil, b) {
		return nil, ErrLoggerInstalled
	}
	return func() { installed.CompareAndSwap(b, nil) }, nil
}

// Enabled reports whether preparing an ordinary event is useful. Implementations
// without the optional capability remain enabled. Unbound diagnostics retain
// their existing Info-and-above stderr behavior.
func Enabled(level Level, stream Stream) bool {
	if current := installed.Load(); current != nil {
		return current.levels == nil || current.levels.Enabled(level, stream)
	}
	return level >= LevelInfo
}

// Log forwards an event. Before installation and after release, diagnostics go
// only to stderr: no default SDK, file creation or notification occurs.
func Log(ctx context.Context, entry LogEntry) {
	if current := installed.Load(); current != nil {
		if !entry.Alert && current.levels != nil && !current.levels.Enabled(entry.Level, entry.Stream) {
			return
		}
		current.logger.Log(ctx, entry)
		return
	}
	if entry.Level >= LevelInfo {
		writeDiagnostic(entry.Text())
	}
}

// Drain invokes only the selected owner's cleanup. Ordinary consumers receive
// Logger, not this lifecycle callback. It is shared by Stop and legacy draining.
func Drain(ctx context.Context) error {
	var err error
	if failure := fallbackFailure.Swap(nil); failure != nil {
		err = failure.err
	}
	if current := installed.Load(); current != nil && current.drain != nil {
		err = errors.Join(err, current.drain(ctx))
	}
	return err
}

// Sync keeps the existing process cleanup call compatible until stage 3 wiring.
func Sync() {
	if err := Drain(diagnosticContext); err != nil {
		writeDiagnostic("drain project logs: " + err.Error())
	}
}

// SyncAccessLog shares the same owner and drains access before main exactly once.
func SyncAccessLog() { Sync() }

func emit(ctx context.Context, level Level, msg string, alert bool, fields []Field) {
	Log(ctx, LogEntry{Level: level, Message: msg, Fields: fields, Alert: alert, CallerSkip: 3})
}

func Debug(msg string, fields ...Field) {
	emitProcess(LogEntry{Level: LevelDebug, Message: msg, Fields: fields})
}
func Info(msg string, fields ...Field) {
	emitProcess(LogEntry{Level: LevelInfo, Message: msg, Fields: fields})
}
func Warn(msg string, fields ...Field) {
	emitProcess(LogEntry{Level: LevelWarn, Message: msg, Fields: fields})
}
func Error(msg string, fields ...Field) {
	emitProcess(LogEntry{Level: LevelError, Message: msg, Fields: fields})
}
func Debugf(format string, args ...any) {
	emitProcess(LogEntry{Level: LevelDebug, Message: format, Format: true, Args: args, Alert: false})
}
func Infof(format string, args ...any) {
	emitProcess(LogEntry{Level: LevelInfo, Message: format, Format: true, Args: args, Alert: false})
}
func Warnf(format string, args ...any) {
	emitProcess(LogEntry{Level: LevelWarn, Message: format, Format: true, Args: args, Alert: false})
}
func Errorf(format string, args ...any) {
	emitProcess(LogEntry{Level: LevelError, Message: format, Format: true, Args: args, Alert: false})
}
func HErrorf(format string, args ...any) {
	emitProcess(LogEntry{Level: LevelError, Message: format, Format: true, Args: args, Alert: true})
}

// emitProcess preserves the existing context-free process diagnostic API.
// Request-aware variants bypass it and propagate the caller context through emit.
//
//nolint:contextcheck,nolintlint // contextcheck consumes this function annotation as an analysis boundary; it emits no local issue for nolintlint to count.
func emitProcess(entry LogEntry) {
	entry.CallerSkip = 3
	Log(diagnosticContext, entry)
}
func WarnCtx(ctx context.Context, msg string, fields ...Field) {
	emit(ctx, LevelWarn, msg, false, fields)
}
func ErrorCtx(ctx context.Context, msg string, fields ...Field) {
	emit(ctx, LevelError, msg, false, fields)
}

func String(key, value string) Field                 { return Field{key, value} }
func Int(key string, value int) Field                { return Field{key, value} }
func Int32(key string, value int32) Field            { return Field{key, value} }
func Int64(key string, value int64) Field            { return Field{key, value} }
func Duration(key string, value time.Duration) Field { return Field{key, value} }
func Any(key string, value any) Field                { return Field{key, value} }
func Err(err error) Field                            { return NamedError("error", err) }
func NamedError(key string, err error) Field {
	if err == nil {
		return Field{}
	}
	return Field{key, err}
}
func Stack(key string) Field { return Field{key, string(debug.Stack())} }

type requestIDKey struct{}

// ContextWithRequestID binds the generated internal request ID without altering
// cancellation or deadlines. It never trusts an inbound header by itself.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFromContext retrieves the ID used by the project logging boundary.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}
