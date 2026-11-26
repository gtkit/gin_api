package bootstrap

// buildWorkers 组装进程级后台任务。
func buildWorkers(inf *infra, svc services) (*WorkerManager, error) {
	workers, err := optionalWorkers(inf)
	if err != nil {
		return nil, err
	}
	workers = append(workers, fileRecoveryWorkers(svc)...)

	manager, err := NewWorkerManager(DefaultWorkerBudget(), workers...)
	if err != nil {
		return nil, err
	}
	manager.recorder = inf.recorder
	return manager, nil
}
