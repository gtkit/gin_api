package core

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	// ErrPluginContractViolation reports an invalid plugin or registration.
	ErrPluginContractViolation = errors.New("plugin contract violation")
	// ErrPluginRegistryLifecycle reports an operation invalid in the current state.
	ErrPluginRegistryLifecycle = errors.New("plugin registry lifecycle has started")
	// ErrPluginDependencyMissing reports an unregistered dependency.
	ErrPluginDependencyMissing = errors.New("plugin dependency is missing")
	// ErrPluginDependencyCycle reports a cycle in the dependency graph.
	ErrPluginDependencyCycle = errors.New("plugin dependency cycle")
)

const (
	pluginStartupBudget  = 30 * time.Second
	pluginCallbackBudget = 10 * time.Second
	pluginRollbackBudget = 30 * time.Second
	pluginStopBudget     = 30 * time.Second
)

type registryState uint8

const (
	registryRegistering registryState = iota
	registryInitializing
	registryInitialized
	registryStarting
	registryStarted
	registryStopping
	registryStopped
	registryFailed
)

type registeredPlugin struct {
	name         string
	plugin       Plugin
	dependencies []string
}

type lifecycleOperation struct {
	done chan struct{}
	err  error // Published before closing done; readers must wait for done.
}

func (o *lifecycleOperation) await(ctx context.Context, forced <-chan struct{}) error {
	// An already published result takes priority over cancellation.
	select {
	case <-o.done:
		return nil
	default:
	}
	select {
	case <-o.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-forced:
		return context.Canceled
	}
}

func (o *lifecycleOperation) wait(ctx context.Context) error {
	if err := o.await(ctx, nil); err != nil {
		return err
	}
	return o.err
}

// PluginRegistry schedules compile-time plugins. Its zero value is ready to use.
//
// Registration order breaks ties between ready plugins. Callbacks run outside
// the mutex and must not reenter this registry. In-flight callers share results;
// a waiting caller can cancel its own wait without canceling the owner. Stop
// attempts each entered plugin once and caches failures, rather than retrying a
// possibly destructive close. A stopped or failed registry cannot be restarted.
// A registry must not be copied after first use.
type PluginRegistry struct {
	mu sync.Mutex

	state           registryState
	plugins         map[string]registeredPlugin
	registration    []string
	order           []registeredPlugin
	operation       *lifecycleOperation
	startOperation  *lifecycleOperation // Covers implicit Init and Start together.
	startupDeadline time.Time
	forceSignal     <-chan struct{}

	initErr  error
	startErr error
	stopErr  error
}

// NewPluginRegistry creates an empty registry.
func NewPluginRegistry() *PluginRegistry {
	return &PluginRegistry{}
}

// SetForceSignal installs an optional process-level force signal before Init or
// Start. Closing it cancels cleanup, including rollback detached from startup.
// The caller owns the channel; the registry never closes it.
func (r *PluginRegistry) SetForceSignal(ch <-chan struct{}) error {
	if r == nil {
		return fmt.Errorf("set plugin force signal: %w: registry is nil", ErrPluginContractViolation)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != registryRegistering {
		return fmt.Errorf("set plugin force signal: %w", ErrPluginRegistryLifecycle)
	}
	r.forceSignal = ch
	return nil
}

// Register freezes a plugin's normalized name and copies dependency names.
// It performs no Init, Start or Stop callbacks. Context arguments to lifecycle
// methods must be non-nil, as with other context-aware Go APIs.
func (r *PluginRegistry) Register(plugin Plugin, dependencies ...string) error {
	if r == nil {
		return fmt.Errorf("register plugin: %w: registry is nil", ErrPluginContractViolation)
	}
	if isNilPlugin(plugin) {
		return fmt.Errorf("register plugin: %w: plugin is nil", ErrPluginContractViolation)
	}
	name := strings.TrimSpace(plugin.Name())
	if name == "" {
		return fmt.Errorf("register plugin: %w: name is empty", ErrPluginContractViolation)
	}
	deps := make([]string, 0, len(dependencies))
	seen := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		dependency = strings.TrimSpace(dependency)
		if dependency == "" {
			return fmt.Errorf("register plugin %q: %w: dependency name is empty", name, ErrPluginContractViolation)
		}
		if _, duplicate := seen[dependency]; duplicate {
			return fmt.Errorf("register plugin %q: %w: duplicate dependency %q", name, ErrPluginContractViolation, dependency)
		}
		seen[dependency] = struct{}{}
		deps = append(deps, dependency)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != registryRegistering {
		return fmt.Errorf("register plugin %q: %w", name, ErrPluginRegistryLifecycle)
	}
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("register plugin %q: %w: duplicate name", name, ErrPluginContractViolation)
	}
	if r.plugins == nil {
		r.plugins = make(map[string]registeredPlugin)
	}
	r.plugins[name] = registeredPlugin{name: name, plugin: plugin, dependencies: deps}
	r.registration = append(r.registration, name)
	return nil
}

// Init validates the entire graph before initializing in dependency order.
// Startup shares a 30-second deadline with Start, with at most 10 seconds per
// callback. Failure rolls back the entered prefix with an independent 30-second
// budget. Context timeouts require cooperative callbacks; callbacks run inline.
func (r *PluginRegistry) Init(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	switch r.state {
	case registryRegistering:
		op, err := r.beginInitLocked(ctx)
		r.mu.Unlock()
		if err != nil {
			return err
		}
		return r.runInit(ctx, op)
	case registryInitializing:
		op := r.operation
		r.mu.Unlock()
		return op.wait(ctx)
	case registryFailed:
		err := errors.Join(r.initErr, r.startErr)
		r.mu.Unlock()
		return err
	default:
		r.mu.Unlock()
		return fmt.Errorf("init plugin registry: %w", ErrPluginRegistryLifecycle)
	}
}

// beginInitLocked validates and freezes execution data. The caller holds mu.
func (r *PluginRegistry) beginInitLocked(ctx context.Context) (*lifecycleOperation, error) {
	order, err := r.topologicalOrderLocked()
	// The graph is no longer needed after validation; don't retain its edges.
	r.plugins = nil
	r.registration = nil
	if err != nil {
		r.initErr = err
		r.state = registryFailed
		return nil, err
	}
	r.startupDeadline = time.Now().Add(pluginStartupBudget)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(r.startupDeadline) {
		r.startupDeadline = deadline
	}
	r.order = order
	r.operation = &lifecycleOperation{done: make(chan struct{})}
	r.state = registryInitializing
	return r.operation, nil
}

func (r *PluginRegistry) runInit(ctx context.Context, op *lifecycleOperation) error {
	startupCtx, cancel := context.WithDeadline(ctx, r.startupDeadline)
	defer cancel()
	entered := 0
	var root error
	for _, entry := range r.order {
		if root = startupCtx.Err(); root != nil {
			break
		}
		entered++ // Init may acquire resources before returning an error or panic.
		if root = callStartupPlugin(startupCtx, entry, "init", entry.plugin.Init); root != nil {
			break
		}
	}
	root = withContextError(startupCtx, root)
	var cleanupErr error
	if root != nil {
		cleanupErr = r.rollback(ctx, r.order[:entered])
	}
	fullErr := errors.Join(root, cleanupErr)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initErr, r.stopErr = fullErr, cleanupErr
	r.state = registryInitialized
	if fullErr != nil {
		r.state = registryFailed
		r.order = nil
	}
	r.operation = nil
	op.err = fullErr
	close(op.done)
	return fullErr
}

// Start reserves one operation before implicit Init so concurrent Start callers
// join the entire startup sequence. Explicit Init and Start share the original
// startup deadline. Any Start failure rolls back ALL initialized plugins.
func (r *PluginRegistry) Start(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.startOperation != nil {
		op := r.startOperation
		r.mu.Unlock()
		return op.wait(ctx)
	}
	var initOp *lifecycleOperation
	var ownsInit bool
	switch r.state {
	case registryRegistering:
		var err error
		initOp, err = r.beginInitLocked(ctx)
		if err != nil {
			r.mu.Unlock()
			return err
		}
		ownsInit = true
	case registryInitializing:
		initOp = r.operation
	case registryInitialized:
	case registryFailed:
		err := errors.Join(r.initErr, r.startErr)
		r.mu.Unlock()
		return err
	default:
		r.mu.Unlock()
		return fmt.Errorf("start plugin registry: %w", ErrPluginRegistryLifecycle)
	}
	op := &lifecycleOperation{done: make(chan struct{})}
	r.startOperation = op
	r.mu.Unlock()

	var initErr error
	if ownsInit {
		initErr = r.runInit(ctx, initOp)
	} else if initOp != nil {
		initErr = initOp.wait(ctx)
	}
	if initErr != nil {
		// A caller waiting for someone else's Init must not take over its cleanup.
		r.mu.Lock()
		defer r.mu.Unlock()
		r.startOperation = nil
		op.err = fmt.Errorf("start plugin registry: %w", initErr)
		close(op.done)
		return op.err
	}

	r.mu.Lock()
	r.state = registryStarting
	r.operation = op
	r.mu.Unlock()
	startupCtx, cancel := context.WithDeadline(ctx, r.startupDeadline)
	defer cancel()
	var root error
	for _, entry := range r.order {
		if root = startupCtx.Err(); root != nil {
			break
		}
		if root = callStartupPlugin(startupCtx, entry, "start", entry.plugin.Start); root != nil {
			break
		}
	}
	root = withContextError(startupCtx, root)
	var cleanupErr error
	if root != nil {
		cleanupErr = r.rollback(ctx, r.order)
	}
	fullErr := errors.Join(root, cleanupErr)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startErr, r.stopErr = fullErr, cleanupErr
	r.state = registryStarted
	if fullErr != nil {
		r.state = registryFailed
		r.order = nil
	}
	r.operation, r.startOperation = nil, nil
	op.err = fullErr
	close(op.done)
	return fullErr
}

// Stop waits for startup and stops entered plugins in reverse dependency order.
// Waiting and callbacks share one 30-second upper limit, tightened by ctx.
// Cancellation of a joiner's wait does not cancel or duplicate the owner's work.
// A timeout or close failure remains visible on later Stop calls.
func (r *PluginRegistry) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	stopCtx, cancel := context.WithTimeout(ctx, pluginStopBudget)
	defer cancel()
	for {
		r.mu.Lock()
		op := r.startOperation
		if op == nil && r.state == registryInitializing {
			op = r.operation
		}
		if op != nil {
			forced := r.forceSignal
			r.mu.Unlock()
			if err := op.await(stopCtx, forced); err != nil {
				return fmt.Errorf("wait to stop plugin registry: %w", err)
			}
			continue
		}
		switch r.state {
		case registryRegistering:
			r.plugins, r.registration = nil, nil
			r.state = registryStopped
			r.mu.Unlock()
			return nil
		case registryInitialized, registryStarted:
			op = &lifecycleOperation{done: make(chan struct{})}
			r.operation = op
			r.state = registryStopping
			order, forced := r.order, r.forceSignal
			r.mu.Unlock()
			cleanupCtx, finish := forceCleanupContext(stopCtx, forced)
			err := stopPlugins(cleanupCtx, order)
			finish()
			r.mu.Lock()
			r.stopErr = err
			r.state = registryStopped
			r.order, r.operation = nil, nil
			op.err = err
			close(op.done)
			r.mu.Unlock()
			return err
		case registryStopping:
			op, forced := r.operation, r.forceSignal
			r.mu.Unlock()
			if err := op.await(stopCtx, forced); err != nil {
				return err
			}
			return op.err
		case registryFailed, registryStopped:
			err := r.stopErr
			r.mu.Unlock()
			return err
		default:
			r.mu.Unlock()
			return fmt.Errorf("stop plugin registry: %w", ErrPluginRegistryLifecycle)
		}
	}
}

func (r *PluginRegistry) rollback(ctx context.Context, order []registeredPlugin) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pluginRollbackBudget)
	defer cancel()
	cleanupCtx, finish := forceCleanupContext(rollbackCtx, r.forceSignal)
	defer finish()
	return stopPlugins(cleanupCtx, order)
}

// forceCleanupContext owns and joins its sole observer. It never runs a plugin
// in a goroutine: ignoring ctx cannot leave an abandoned callback behind.
func forceCleanupContext(ctx context.Context, forced <-chan struct{}) (context.Context, func()) {
	if forced == nil {
		return ctx, func() {}
	}
	cleanupCtx, cancel := context.WithCancel(ctx)
	select {
	case <-forced:
		cancel()
		return cleanupCtx, cancel
	default:
	}
	var observer sync.WaitGroup
	observer.Go(func() {
		select {
		case <-forced:
			cancel()
		case <-cleanupCtx.Done():
		}
	})
	return cleanupCtx, func() {
		cancel()
		observer.Wait()
	}
}

func callStartupPlugin(ctx context.Context, entry registeredPlugin, phase string, callback func(context.Context) error) error {
	callbackCtx, cancel := context.WithTimeout(ctx, pluginCallbackBudget)
	defer cancel()
	return withContextError(callbackCtx, callPlugin(callbackCtx, entry.name, phase, callback))
}

func callPlugin(ctx context.Context, name, phase string, callback func(context.Context) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%s plugin %q: %w: panic: %v", phase, name, ErrPluginContractViolation, recovered)
		}
	}()
	if err = callback(ctx); err != nil {
		return fmt.Errorf("%s plugin %q: %w", phase, name, err)
	}
	return nil
}

func stopPlugins(ctx context.Context, order []registeredPlugin) error {
	var errs []error
	for _, entry := range slices.Backward(order) {
		if err := callPlugin(ctx, entry.name, "stop", entry.plugin.Stop); err != nil {
			errs = append(errs, err)
		}
	}
	return withContextError(ctx, errors.Join(errs...))
}

func withContextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
		return errors.Join(err, ctxErr)
	}
	return err
}

func (r *PluginRegistry) topologicalOrderLocked() ([]registeredPlugin, error) {
	indegree := make(map[string]int, len(r.registration))
	dependents := make(map[string][]int, len(r.registration))
	ready := make(registrationQueue, 0, len(r.registration))
	for i, name := range r.registration {
		entry := r.plugins[name]
		indegree[name] = len(entry.dependencies)
		for _, dependency := range entry.dependencies {
			if _, exists := r.plugins[dependency]; !exists {
				return nil, fmt.Errorf("plugin %q: %w: %q", name, ErrPluginDependencyMissing, dependency)
			}
			dependents[dependency] = append(dependents[dependency], i)
		}
		if indegree[name] == 0 {
			ready = append(ready, i)
		}
	}
	heap.Init(&ready)
	order := make([]registeredPlugin, 0, len(r.registration))
	for ready.Len() > 0 {
		//nolint:errcheck // The private heap contains only int registration indexes.
		i := heap.Pop(&ready).(int)
		name := r.registration[i]
		entry := r.plugins[name]
		entry.dependencies = nil
		order = append(order, entry)
		for _, dependent := range dependents[name] {
			dependentName := r.registration[dependent]
			indegree[dependentName]--
			if indegree[dependentName] == 0 {
				heap.Push(&ready, dependent)
			}
		}
	}
	if len(order) != len(r.registration) {
		return nil, fmt.Errorf("%w: %s", ErrPluginDependencyCycle,
			strings.Join(findCycle(r.plugins, r.registration, indegree), " -> "))
	}
	return order, nil
}

// Every unresolved node has an unresolved dependency. Following those edges
// reaches a cycle without recursion, even for a deeply nested invalid graph.
func findCycle(plugins map[string]registeredPlugin, registration []string, indegree map[string]int) []string {
	var name string
	for _, candidate := range registration {
		if indegree[candidate] > 0 {
			name = candidate
			break
		}
	}
	positions := make(map[string]int, len(registration))
	path := make([]string, 0, len(registration))
	for {
		if start, seen := positions[name]; seen {
			return append(path[start:], name)
		}
		positions[name] = len(path)
		path = append(path, name)
		for _, dependency := range plugins[name].dependencies {
			if indegree[dependency] > 0 {
				name = dependency
				break
			}
		}
	}
}

type registrationQueue []int

func (q registrationQueue) Len() int           { return len(q) }
func (q registrationQueue) Less(i, j int) bool { return q[i] < q[j] }
func (q registrationQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *registrationQueue) Push(value any) {
	//nolint:errcheck // container/heap requires any; only this registry pushes int indexes.
	*q = append(*q, value.(int))
}
func (q *registrationQueue) Pop() any {
	last := len(*q) - 1
	value := (*q)[last]
	*q = (*q)[:last]
	return value
}

func isNilPlugin(plugin Plugin) bool {
	if plugin == nil {
		return true
	}
	value := reflect.ValueOf(plugin)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
