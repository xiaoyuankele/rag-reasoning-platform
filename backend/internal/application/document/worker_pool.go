package document

import (
	"context"
	"errors"
	"sync"
)

var (
	// ErrWorkerPoolLoopRequired 表示 Worker 池没有可以运行的 WorkerLoop。
	ErrWorkerPoolLoopRequired = errors.New("worker pool requires a worker loop")

	// ErrInvalidWorkerPoolConcurrency 表示 Worker 数量不是正整数。
	ErrInvalidWorkerPoolConcurrency = errors.New(
		"worker pool concurrency must be positive",
	)
)

// workerLoopRunner 是 WorkerPool 启动循环所需的最小契约。
// 生产环境由 WorkerLoop 满足，测试环境可以使用可控 Fake。
type workerLoopRunner interface {
	Run(ctx context.Context)
}

var _ workerLoopRunner = (*WorkerLoop)(nil)

// WorkerPool 在同一后端进程内运行固定数量的 WorkerLoop。
//
// 每个循环一次只处理一个 ProcessingJob。循环可以共享同一个 WorkerLoop
// 实例，因为 WorkerLoop 和 Worker 不保存单次任务状态；任务数据、Python
// 命令和输出缓冲区都在每次 RunOnce 调用内部创建。
type WorkerPool struct {
	loop        workerLoopRunner
	concurrency int
}

// NewWorkerPool 创建有界 Worker 池。
func NewWorkerPool(
	loop workerLoopRunner,
	concurrency int,
) (*WorkerPool, error) {
	if loop == nil {
		return nil, ErrWorkerPoolLoopRequired
	}
	if concurrency <= 0 {
		return nil, ErrInvalidWorkerPoolConcurrency
	}

	return &WorkerPool{
		loop:        loop,
		concurrency: concurrency,
	}, nil
}

// Run 启动固定数量的 WorkerLoop，并阻塞等待全部循环退出。
//
// ctx 取消后，每个循环都会停止等待或取消当前处理；只有所有子 goroutine
// 都退出，Run 才返回，从而让 main 的 shutdown 不会提前关闭数据库连接池。
func (p *WorkerPool) Run(ctx context.Context) {
	var group sync.WaitGroup
	group.Add(p.concurrency)

	for range p.concurrency {
		go func() {
			defer group.Done()
			p.loop.Run(ctx)
		}()
	}

	group.Wait()
}
