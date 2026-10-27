//go:build ignore

// load-api is a bounded arrival-rate generator. Run and test it by file list.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maxScenarioBytes = 1 << 20

var latencyBounds = []time.Duration{time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}

type loadScenario struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Weight  int               `json:"weight"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type loadConfig struct {
	rate, concurrency int
	duration, timeout time.Duration
	allowRemote       bool
}

type loadSummary struct {
	Offered          uint64         `json:"offered"`
	Sent             uint64         `json:"sent"`
	Completed        uint64         `json:"completed"`
	Dropped          uint64         `json:"generator_dropped"`
	SchedulerDropped uint64         `json:"scheduler_dropped"`
	WorkerDropped    uint64         `json:"worker_dropped"`
	TransportErrors  uint64         `json:"transport_errors"`
	Status           map[int]uint64 `json:"status"`
	ElapsedSeconds   float64        `json:"elapsed_seconds"`
	Latency          latencySummary `json:"scheduled_latency"`
}

type latencySummary struct {
	BoundsMS    []float64 `json:"bounds_ms"`
	Counts      []uint64  `json:"counts"`
	P95         *float64  `json:"p95_upper_ms"`
	P99         *float64  `json:"p99_upper_ms"`
	P95Overflow bool      `json:"p95_overflow"`
	P99Overflow bool      `json:"p99_overflow"`
}

type loadStats struct {
	mu      sync.Mutex
	summary loadSummary
	buckets []uint64
}
type loadJob struct {
	scheduled time.Time
	scenario  loadScenario
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := loadCLI(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func loadCLI(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("load-api", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var target, scenarioFile string
	var cfg loadConfig
	fs.StringVar(&target, "target", "", "explicit http(s) origin (loopback by default)")
	fs.StringVar(&scenarioFile, "scenario", "", "JSON file containing request scenarios")
	fs.IntVar(&cfg.rate, "rate", 10, "offered arrivals per second, 1..100000")
	fs.IntVar(&cfg.concurrency, "concurrency", 4, "workers, 1..4096")
	fs.DurationVar(&cfg.duration, "duration", 10*time.Second, "generation duration, positive and at most 24h; in-flight requests drain afterward")
	fs.DurationVar(&cfg.timeout, "timeout", 5*time.Second, "request timeout, positive and at most 1m")
	fs.BoolVar(&cfg.allowRemote, "allow-remote", false, "explicitly permit non-loopback targets")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	base, err := validateLoadConfig(target, cfg)
	if err != nil {
		return err
	}
	if scenarioFile == "" {
		return errors.New("scenario file is required")
	}
	f, err := os.Open(scenarioFile)
	if err != nil {
		return errors.New("cannot open scenario file")
	}
	scenarios, readErr := readScenarios(f)
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	client, transport := loadHTTPClient(cfg)
	defer transport.CloseIdleConnections()
	summary := runLoad(ctx, client, base, scenarios, cfg)
	return json.NewEncoder(out).Encode(summary)
}

func validateLoadConfig(target string, cfg loadConfig) (*url.URL, error) {
	if cfg.rate < 1 || cfg.rate > 100000 || cfg.concurrency < 1 || cfg.concurrency > 4096 || cfg.duration <= 0 || cfg.duration > 24*time.Hour || cfg.timeout <= 0 || cfg.timeout > time.Minute {
		return nil, errors.New("load parameters exceed documented bounds")
	}
	u, err := url.Parse(target)
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("target must be an http(s) origin without credentials, query or path")
	}
	if !cfg.allowRemote && !loopbackHost(u.Hostname()) {
		return nil, errors.New("non-loopback target requires -allow-remote")
	}
	return u, nil
}

func loopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func readScenarios(reader io.Reader) ([]loadScenario, error) {
	b, err := io.ReadAll(io.LimitReader(reader, maxScenarioBytes+1))
	if err != nil {
		return nil, errors.New("cannot read scenario file")
	}
	if len(b) > maxScenarioBytes {
		return nil, errors.New("scenario file exceeds 1 MiB")
	}
	var scenarios []loadScenario
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&scenarios); err != nil {
		return nil, errors.New("invalid scenario JSON")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("scenario file must contain one JSON array")
	}
	if len(scenarios) == 0 || len(scenarios) > 64 {
		return nil, errors.New("scenario count must be 1..64")
	}
	for i := range scenarios {
		s := &scenarios[i]
		switch s.Method {
		case "GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS", "PATCH":
		default:
			return nil, errors.New("unsupported scenario method")
		}
		u, err := url.Parse(s.Path)
		if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(s.Path, "/") || strings.HasPrefix(s.Path, "//") {
			return nil, errors.New("scenario path must be an origin-relative request path")
		}
		if s.Weight == 0 {
			s.Weight = 1
		}
		if s.Weight < 1 || s.Weight > 10000 || len(s.Body) > 64<<10 || len(s.Headers) > 32 {
			return nil, errors.New("scenario exceeds weight/body/header bounds")
		}
		for key, value := range s.Headers {
			if len(key) > 256 || len(value) > 4096 || strings.EqualFold(key, "Host") || strings.ContainsAny(key+value, "\r\n") {
				return nil, errors.New("invalid scenario header")
			}
		}
	}
	return scenarios, nil
}

func loadHTTPClient(cfg loadConfig) (*http.Client, *http.Transport) {
	dialer := &net.Dialer{Timeout: cfg.timeout}
	transport := &http.Transport{MaxConnsPerHost: cfg.concurrency, MaxIdleConns: cfg.concurrency, MaxIdleConnsPerHost: cfg.concurrency, IdleConnTimeout: time.Minute, TLSHandshakeTimeout: cfg.timeout, ResponseHeaderTimeout: cfg.timeout}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if !cfg.allowRemote {
			if !loopbackHost(host) {
				return nil, errors.New("non-loopback dial rejected")
			}
			if strings.EqualFold(host, "localhost") {
				address = net.JoinHostPort("127.0.0.1", port)
			}
		}
		return dialer.DialContext(ctx, network, address)
	}
	return &http.Client{Transport: transport, Timeout: cfg.timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, transport
}

func scheduledCount(elapsed, duration time.Duration, rate int) int64 {
	total := (duration.Nanoseconds()*int64(rate) + int64(time.Second) - 1) / int64(time.Second)
	if elapsed < 0 {
		return 0
	}
	elapsed = min(elapsed, duration)
	return min(total, elapsed.Nanoseconds()*int64(rate)/int64(time.Second)+1)
}

func runLoad(ctx context.Context, client *http.Client, base *url.URL, scenarios []loadScenario, cfg loadConfig) loadSummary {
	return runLoadWithClock(ctx, client, base, scenarios, cfg, time.Now)
}

func runLoadWithClock(ctx context.Context, client *http.Client, base *url.URL, scenarios []loadScenario, cfg loadConfig, now func() time.Time) loadSummary {
	start := now()
	runCtx, cancel := context.WithTimeout(ctx, cfg.duration)
	defer cancel()
	stats := loadStats{summary: loadSummary{Status: make(map[int]uint64)}, buckets: make([]uint64, len(latencyBounds)+1)}
	jobs := make(chan loadJob)
	var workers sync.WaitGroup
	for range cfg.concurrency {
		workers.Go(func() {
			for job := range jobs {
				stats.request(ctx, client, base, job, cfg.timeout)
			}
		})
	}
	var offered, sent, schedulerDropped, workerDropped int64
	weight := 0
	for _, s := range scenarios {
		weight += s.Weight
	}
	end := start.Add(cfg.duration)
	for runCtx.Err() == nil {
		current := now()
		// Taking the next sample may resume after the generation deadline.
		if !current.Before(end) || runCtx.Err() != nil {
			break
		}
		due := scheduledCount(current.Sub(start), cfg.duration, cfg.rate)
		if due > offered {
			// Only the newest due arrival may be sent. Earlier overdue arrivals
			// were never generated; preserve their offered and dropped counts.
			schedulerDropped += due - offered - 1
			offered = due
			index := int((offered - 1) % int64(weight))
			scenario := scenarios[0]
			for _, s := range scenarios {
				if index < s.Weight {
					scenario = s
					break
				}
				index -= s.Weight
			}
			job := loadJob{start.Add(time.Duration(offered-1) * time.Second / time.Duration(cfg.rate)), scenario}
			select {
			case jobs <- job:
				sent++
			default:
				workerDropped++
			}
		}
		next := start.Add(time.Duration(offered) * time.Second / time.Duration(cfg.rate))
		if next.After(end) {
			next = end
		}
		timer := time.NewTimer(max(time.Until(next), 0))
		select {
		case <-timer.C:
		case <-runCtx.Done():
			timer.Stop()
		}
	}
	remaining := scheduledCount(min(now().Sub(start), cfg.duration), cfg.duration, cfg.rate) - offered
	if remaining > 0 {
		offered += remaining
		schedulerDropped += remaining
	}
	close(jobs)
	workers.Wait()
	stats.summary.Offered = uint64(offered)
	stats.summary.Sent = uint64(sent)
	stats.summary.SchedulerDropped = uint64(schedulerDropped)
	stats.summary.WorkerDropped = uint64(workerDropped)
	stats.summary.Dropped = stats.summary.SchedulerDropped + stats.summary.WorkerDropped
	stats.summary.ElapsedSeconds = now().Sub(start).Seconds()
	stats.summary.Latency = latencyResult(stats.buckets)
	return stats.summary
}

func (s *loadStats) request(ctx context.Context, client *http.Client, base *url.URL, job loadJob, timeout time.Duration) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rel, err := url.Parse(job.scenario.Path)
	var status int
	if err == nil {
		var req *http.Request
		req, err = http.NewRequestWithContext(requestCtx, job.scenario.Method, base.ResolveReference(rel).String(), strings.NewReader(job.scenario.Body))
		if err == nil {
			for key, value := range job.scenario.Headers {
				req.Header.Set(key, value)
			}
			var response *http.Response
			response, err = client.Do(req)
			if response != nil {
				status = response.StatusCode
				n, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, (1<<20)+1))
				closeErr := response.Body.Close()
				err = errors.Join(err, readErr, closeErr)
				if n > 1<<20 {
					err = errors.Join(err, errors.New("response body exceeds limit"))
				}
			}
		}
	}
	elapsed := max(time.Since(job.scheduled), 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.summary.Completed++
	if err != nil {
		s.summary.TransportErrors++
	}
	if status != 0 {
		s.summary.Status[status]++
	}
	i, _ := slices.BinarySearch(latencyBounds, elapsed)
	s.buckets[i]++
}

func latencyResult(counts []uint64) latencySummary {
	result := latencySummary{Counts: slices.Clone(counts)}
	for _, d := range latencyBounds {
		result.BoundsMS = append(result.BoundsMS, float64(d)/float64(time.Millisecond))
	}
	var total uint64
	for _, n := range counts {
		total += n
	}
	quantile := func(q float64) (*float64, bool) {
		if total == 0 {
			return nil, false
		}
		target := uint64(math.Ceil(float64(total) * q))
		var n uint64
		for i, count := range counts {
			n += count
			if n >= target {
				if i == len(latencyBounds) {
					return nil, true
				}
				return new(result.BoundsMS[i]), false
			}
		}
		return nil, false
	}
	result.P95, result.P95Overflow = quantile(.95)
	result.P99, result.P99Overflow = quantile(.99)
	return result
}
