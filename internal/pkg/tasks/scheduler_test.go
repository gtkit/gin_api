package tasks

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/asynqx"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
)

func schedulerConfig() config.TasksConfig {
	cfg := enabledConfig()
	cfg.Scheduler.Enabled = true
	return cfg
}

func newTestRedis(t *testing.T) (*miniredis.Miniredis, redis.UniversalClient) {
	t.Helper()
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("client.Close(): %v", err)
		}
	})
	return srv, client
}

func periodicTask() PeriodicTask {
	return PeriodicTask{
		Spec:     "@every 1h",
		TaskType: "cleanup:run",
		Payload:  map[string]string{"scope": "all"},
	}
}

func TestInvalidPeriodicTasksAreRejectedAtConstruction(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*PeriodicTask)
		want   string
	}{
		{"empty spec", func(p *PeriodicTask) { p.Spec = "  " }, "spec must not be empty"},
		{"empty task type", func(p *PeriodicTask) { p.TaskType = "" }, "task type must not be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, client := newTestRedis(t)
			task := periodicTask()
			tc.mutate(&task)
			_, err := NewScheduler(schedulerConfig(), client, "scheduler", task)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewScheduler error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDisabledSchedulerBuildsNothing(t *testing.T) {
	t.Parallel()
	queueOff := schedulerConfig()
	queueOff.Enabled = false
	schedulerOff := schedulerConfig()
	schedulerOff.Scheduler.Enabled = false
	for _, tc := range []struct {
		name    string
		cfg     config.TasksConfig
		entries []PeriodicTask
	}{
		{"queue disabled", queueOff, []PeriodicTask{periodicTask()}},
		{"scheduler disabled", schedulerOff, []PeriodicTask{periodicTask()}},
		{"no entries", schedulerConfig(), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scheduler, err := NewScheduler(tc.cfg, nil, "scheduler", tc.entries...)
			if err != nil || scheduler != nil {
				t.Fatalf("disabled scheduler = %v, err = %v", scheduler, err)
			}
		})
	}
}

func TestEnabledSchedulerRequiresAClient(t *testing.T) {
	t.Parallel()
	if _, err := NewScheduler(schedulerConfig(), nil, "scheduler", periodicTask()); err == nil {
		t.Fatal("enabled scheduler accepted a nil Redis client")
	}
}

func TestSchedulerPrepareValidatesRegistration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mutate  func(*PeriodicTask)
		wantErr bool
	}{
		{"valid", func(*PeriodicTask) {}, false},
		{"invalid spec", func(p *PeriodicTask) { p.Spec = "not-a-cron-expression" }, true},
		{"invalid payload", func(p *PeriodicTask) { p.Payload = func() {} }, true},
		{"invalid options", func(p *PeriodicTask) {
			p.Options = []asynqx.TaskOption{asynqx.WithTaskUnique(-1)}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, client := newTestRedis(t)
			task := periodicTask()
			tc.mutate(&task)
			scheduler, err := NewScheduler(schedulerConfig(), client, "scheduler", task)
			if err != nil {
				t.Fatal(err)
			}
			err = scheduler.Prepare(t.Context())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Prepare error = %v, wantErr = %t", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), task.TaskType) {
				t.Fatalf("Prepare error does not identify the task: %v", err)
			}
			if err := scheduler.Cleanup(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSchedulerPrepareDoesNotAcquireRedisLease(t *testing.T) {
	t.Parallel()
	srv, client := newTestRedis(t)
	srv.Close()
	scheduler, err := NewScheduler(schedulerConfig(), client, "scheduler", periodicTask())
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Prepare(t.Context()); err != nil {
		t.Fatalf("local registration unexpectedly requires Redis: %v", err)
	}
	if err := scheduler.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerCleanupIsIdempotent(t *testing.T) {
	t.Parallel()
	_, client := newTestRedis(t)
	scheduler, err := NewScheduler(schedulerConfig(), client, "scheduler", periodicTask())
	if err != nil {
		t.Fatal(err)
	}
	// Cleanup also accepts a scheduler that was never prepared.
	if err := scheduler.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := scheduler.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
