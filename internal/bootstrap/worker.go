package bootstrap

import (
	"context"
	"fmt"
	"sync"

	logger "github.com/gtkit/logger/v2"
)

// Worker represents a managed background task.
type Worker interface {
	Name() string
	Start(ctx context.Context) error
}

// WorkerManager starts workers, captures worker failures, and waits for exit.
type WorkerManager struct {
	workers []Worker
	errCh   chan error
	doneCh  chan struct{}
}

func NewWorkerManager(workers ...Worker) *WorkerManager {
	return &WorkerManager{workers: workers}
}

func (m *WorkerManager) Start(ctx context.Context) {
	if m == nil {
		return
	}

	m.errCh = make(chan error, len(m.workers))
	m.doneCh = make(chan struct{})
	if len(m.workers) == 0 {
		close(m.doneCh)
		return
	}

	var wg sync.WaitGroup
	for _, worker := range m.workers {
		if worker == nil {
			continue
		}

		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			logger.Infof("starting worker: %s", w.Name())
			if err := w.Start(ctx); err != nil {
				select {
				case m.errCh <- fmt.Errorf("%s: %w", w.Name(), err):
				default:
				}
			}
		}(worker)
	}

	go func() {
		wg.Wait()
		close(m.doneCh)
	}()
}

func (m *WorkerManager) Errors() <-chan error {
	if m == nil {
		return nil
	}

	return m.errCh
}

func (m *WorkerManager) Wait(ctx context.Context) error {
	if m == nil || m.doneCh == nil {
		return nil
	}

	select {
	case err := <-m.errCh:
		<-m.doneCh
		return err
	case <-m.doneCh:
		select {
		case err := <-m.errCh:
			return err
		default:
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

type legacyWorker struct {
	name  string
	start func(ctx context.Context) error
}

func NewLegacyWorker(name string, start func(ctx context.Context) error) Worker {
	return &legacyWorker{
		name:  name,
		start: start,
	}
}

func (w *legacyWorker) Name() string {
	return w.name
}

func (w *legacyWorker) Start(ctx context.Context) error {
	return w.start(ctx)
}
