package document

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// runOnceWorker 定义循环每次处理一条任务所需的能力。
type runOnceWorker interface {
	RunOnce(
		ctx context.Context,
	) (
		handled bool,
		err error,
	)
}

var _ runOnceWorker = (*Worker)(nil)

var (
	// ErrWorkerLoopWorkerRequired 表示没有提供任务处理器。
	ErrWorkerLoopWorkerRequired = errors.New(
		"worker loop requires a worker",
	)

	// ErrInvalidWorkerPollInterval 表示轮询间隔不是正数。
	ErrInvalidWorkerPollInterval = errors.New(
		"worker poll interval must be positive",
	)

	// ErrWorkerErrorReporterRequired 表示没有提供错误上报函数。
	ErrWorkerErrorReporterRequired = errors.New(
		"worker error reporter is required",
	)
)

// WorkerLoop 控制 Worker 的持续轮询、空闲等待和退出。
type WorkerLoop struct {
	worker       runOnceWorker
	pollInterval time.Duration
	reportError  func(error)
}

// NewWorkerLoop 创建后台任务轮询组件。
func NewWorkerLoop(
	worker runOnceWorker,
	pollInterval time.Duration,
	reportError func(error),
) (*WorkerLoop, error) {
	if worker == nil {
		return nil, ErrWorkerLoopWorkerRequired
	}
	if pollInterval <= 0 {
		return nil, ErrInvalidWorkerPollInterval
	}
	if reportError == nil {
		return nil, ErrWorkerErrorReporterRequired
	}

	return &WorkerLoop{
		worker:       worker,
		pollInterval: pollInterval,
		reportError:  reportError,
	}, nil
}

// waitForNextWorkerPoll 等待下一次轮询。
//
// 返回 true 表示等待时间结束，可以继续；
// 返回 false 表示 ctx 已取消，循环应该退出。
func waitForNextWorkerPoll(
	ctx context.Context,
	pollInterval time.Duration,
) bool {
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Run 持续处理任务，直到 ctx 被取消。
func (l *WorkerLoop) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		handled, err := l.worker.RunOnce(ctx)

		// 停机期间产生的 context.Canceled 属于正常退出，
		// 不需要作为后台处理错误上报。
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			l.reportError(fmt.Errorf("run worker iteration: %w", err))
		}

		// 成功处理一条任务后立即检查下一条；
		// 空队列或错误则进入下方等待，避免忙轮询。
		if err == nil && handled {
			continue
		}

		if !waitForNextWorkerPoll(ctx, l.pollInterval) {
			return
		}
	}
}
