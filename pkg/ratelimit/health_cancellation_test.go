package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestHealthCancellationTreesDoNotHideDependencyFailures(t *testing.T) {
	t.Parallel()
	dependency := errors.New("dependency failed")
	for _, tt := range []struct {
		name string
		err  error
		pure bool
	}{
		{name: "canceled", err: context.Canceled, pure: true},
		{name: "wrapped canceled", err: fmt.Errorf("request: %w", context.Canceled), pure: true},
		{name: "joined canceled", err: errors.Join(nil, context.Canceled, fmt.Errorf("nested: %w", context.Canceled)), pure: true},
		{name: "nested canceled tree", err: fmt.Errorf("outer: %w", errors.Join(context.Canceled, errors.Join(nil, context.Canceled))), pure: true},
		{name: "canceled and dependency", err: errors.Join(context.Canceled, dependency)},
		{name: "wrapped mixed tree", err: fmt.Errorf("outer: %w", errors.Join(context.Canceled, fmt.Errorf("nested: %w", io.EOF)))},
		{name: "deadline is dependency failure", err: context.DeadlineExceeded},
		{name: "canceled and deadline", err: errors.Join(context.Canceled, context.DeadlineExceeded)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newTestHealth(t, HealthPolicy{FailureThreshold: 2})
			s.Observe(dependency)
			s.Observe(tt.err)
			if !tt.pure {
				if s.Phase() != PhaseDegraded {
					t.Fatalf("mixed/dependency failure left phase %s, want degraded", s.Phase())
				}
				return
			}
			if s.Phase() != PhaseHealthy {
				t.Fatalf("pure cancellation changed phase to %s", s.Phase())
			}
			// Cancellation cannot count as success and erase the previous failure.
			s.Observe(dependency)
			if s.Phase() != PhaseDegraded {
				t.Fatalf("cancellation erased the previous failure: phase=%s", s.Phase())
			}
		})
	}
}

func TestHealthCanceledProbePreservesRecoveryThresholdAndInterval(t *testing.T) {
	t.Parallel()
	for _, priorSuccess := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior_success=%v", priorSuccess), func(t *testing.T) {
			t.Parallel()
			policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 2, Cooldown: 30 * time.Second, ProbeInterval: 5 * time.Second}
			s, clock := newTestHealth(t, policy)
			s.Observe(io.EOF)
			clock.advance(policy.Cooldown)
			if priorSuccess {
				attempt, allowed := s.beginAttempt(t.Context())
				if !allowed {
					t.Fatal("first recovery probe denied")
				}
				s.observeAttempt(attempt, nil)
				clock.advance(policy.ProbeInterval)
			}
			attempt, allowed := s.beginAttempt(t.Context())
			if !allowed {
				t.Fatal("cancelable recovery probe denied")
			}
			s.observeAttempt(attempt, fmt.Errorf("request: %w", context.Canceled))
			if s.Phase() != PhaseDegraded {
				t.Fatalf("canceled probe phase=%s, want degraded", s.Phase())
			}
			clock.advance(policy.ProbeInterval - time.Nanosecond)
			if s.AllowRequest(t.Context()) {
				t.Fatal("canceled probe permitted another probe before ProbeInterval")
			}
			clock.advance(time.Nanosecond)
			retry, allowed := s.beginAttempt(t.Context())
			if !allowed {
				t.Fatal("canceled probe restarted Cooldown instead of ProbeInterval, or retained ownership")
			}
			s.observeAttempt(retry, nil)
			want := PhaseDegraded
			if priorSuccess {
				want = PhaseHealthy
			}
			if s.Phase() != want {
				t.Fatalf("phase=%s, want %s: canceled probe altered recovery samples", s.Phase(), want)
			}
		})
	}
}

type controlledRateLimitRunner struct {
	*fakeRedis
	process func(context.Context, redis.Cmder) error
}

func (r controlledRateLimitRunner) Process(ctx context.Context, cmd redis.Cmder) error {
	return r.process(ctx, cmd)
}

func healthTestBackend(client scriptRunner, health *HealthState) *RedisBackend {
	backend := &RedisBackend{client: client, health: health, observer: nopObserver{}}
	backend.sha.Store(new("test-sha"))
	return backend
}

func TestRedisBackendLateResultsCannotReleaseAnotherProbe(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "failure", err: io.EOF},
		{name: "cancellation", err: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: time.Second, ProbeInterval: time.Second}
			health, clock := newTestHealth(t, policy)
			oldEntered, probeEntered := make(chan struct{}), make(chan struct{})
			oldRelease, probeRelease := make(chan struct{}), make(chan struct{})
			unblockOld, unblockProbe := sync.OnceFunc(func() { close(oldRelease) }), sync.OnceFunc(func() { close(probeRelease) })
			defer unblockOld()
			defer unblockProbe()
			client := controlledRateLimitRunner{fakeRedis: &fakeRedis{}, process: func(ctx context.Context, cmd redis.Cmder) error {
				switch cmd.Args()[3] {
				case "old":
					close(oldEntered)
					<-oldRelease
					if tt.err != nil {
						cmd.SetErr(tt.err)
						return tt.err
					}
				case "probe":
					close(probeEntered)
					<-probeRelease
				}
				value, ok := cmd.(interface{ SetVal(any) })
				if !ok {
					return fmt.Errorf("unexpected Redis command %T", cmd)
				}
				value.SetVal([]any{int64(statusAllowed), int64(0), int64(0)})
				return nil
			}}
			backend := healthTestBackend(client, health)
			oldDone := make(chan error, 1)
			go func() {
				_, err := backend.check(t.Context(), redisTestPolicy(), "old")
				oldDone <- err
			}()
			<-oldEntered
			health.Observe(io.ErrUnexpectedEOF)
			clock.advance(policy.Cooldown)
			probeDone := make(chan error, 1)
			go func() {
				_, err := backend.check(t.Context(), redisTestPolicy(), "probe")
				probeDone <- err
			}()
			<-probeEntered
			unblockOld()
			if err := <-oldDone; !errors.Is(err, tt.err) {
				t.Fatalf("late request err=%v, want %v", err, tt.err)
			}
			if health.Phase() != PhaseProbing || health.AllowRequest(t.Context()) {
				t.Fatalf("late %s released or completed someone else's probe: phase=%s", tt.name, health.Phase())
			}
			unblockProbe()
			if err := <-probeDone; err != nil || health.Phase() != PhaseHealthy {
				t.Fatalf("owned probe err=%v phase=%s, want healthy", err, health.Phase())
			}
		})
	}
}

func TestHealthCanceledAttemptCannotCompleteItsReplacement(t *testing.T) {
	t.Parallel()
	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, ProbeInterval: time.Second}
	s, clock := newTestHealth(t, policy)
	s.Observe(io.EOF)
	old, allowed := s.beginAttempt(t.Context())
	if !allowed {
		t.Fatal("first probe denied")
	}
	s.observeAttempt(old, context.Canceled)
	clock.advance(policy.ProbeInterval)
	replacement, allowed := s.beginAttempt(t.Context())
	if !allowed {
		t.Fatal("replacement probe denied")
	}
	for _, late := range []error{nil, context.Canceled, io.EOF} {
		s.observeAttempt(old, late)
		if s.Phase() != PhaseProbing || s.AllowRequest(t.Context()) {
			t.Fatal("canceled attempt changed the replacement probe's ownership")
		}
	}
	s.observeAttempt(replacement, nil)
	if s.Phase() != PhaseHealthy {
		t.Fatalf("replacement phase=%s, want healthy", s.Phase())
	}
}

func TestRedisPreCanceledRequestDoesNotCallOrClaimBackend(t *testing.T) {
	t.Parallel()
	for _, degraded := range []bool{false, true} {
		t.Run(fmt.Sprintf("degraded=%v", degraded), func(t *testing.T) {
			t.Parallel()
			fake := &fakeRedis{}
			health := NewHealthState(HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1}, nil)
			if degraded {
				health.forceDegraded()
			}
			backend := healthTestBackend(fake, health)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := backend.check(ctx, redisTestPolicy(), "canceled")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled request err=%v", err)
			}
			_, calls, _ := fake.counts()
			if calls != 0 || !health.AllowRequest(t.Context()) {
				t.Fatalf("canceled request called Redis %d times or retained probe ownership", calls)
			}
		})
	}
}

func TestFallbackPureCancellationDoesNotSpendShadowTokens(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		err  error
		pure bool
	}{
		{name: "pure", err: fmt.Errorf("request: %w", context.Canceled), pure: true},
		{name: "mixed", err: errors.Join(context.Canceled, io.EOF)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeRedis{evalErrs: []error{tt.err}}
			backend := healthTestBackend(fake, NewHealthState(DefaultHealthPolicy(), nil))
			policy := redisTestPolicy()
			policy.Burst, policy.Rate, policy.FailureMode = 1, 0.01, FailureLocalFallback
			fallback, err := NewFallbackBackend(backend, policy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(fallback.Close)
			decision, err := fallback.Check(t.Context(), "subject")
			if tt.pure {
				if !errors.Is(err, context.Canceled) || decision.Outcome != OutcomeBackendUnavailable {
					t.Fatalf("pure cancellation decision=%s err=%v", decision.Outcome, err)
				}
			} else if err != nil || !decision.Allowed() || !decision.Degraded {
				t.Fatalf("mixed failure did not use fallback: decision=%+v err=%v", decision, err)
			}
			shadow, err := fallback.shadow.Check(t.Context(), "subject")
			if err != nil || shadow.Allowed() != tt.pure {
				t.Fatalf("shadow allowed=%v err=%v, want %v", shadow.Allowed(), err, tt.pure)
			}
		})
	}
}
