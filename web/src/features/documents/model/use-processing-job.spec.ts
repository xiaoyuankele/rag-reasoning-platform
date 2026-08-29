import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ProcessingJob } from '../../../entities/processing-job/model/processing-job'
import { ApiError } from '../../../shared/api/api-error'
import {
  cancelProcessingJob,
  getLatestProcessingJobs,
  getProcessingJob,
  queueDocumentProcessing,
} from '../api/processing-api'
import { useProcessingJob } from './use-processing-job'

vi.mock('../api/processing-api', () => ({
  cancelProcessingJob: vi.fn(),
  getLatestProcessingJobs: vi.fn(),
  getProcessingJob: vi.fn(),
  queueDocumentProcessing: vi.fn(),
}))

const getProcessingJobMock = vi.mocked(getProcessingJob)
const queueDocumentProcessingMock = vi.mocked(queueDocumentProcessing)
const getLatestProcessingJobsMock = vi.mocked(getLatestProcessingJobs)
const cancelProcessingJobMock = vi.mocked(cancelProcessingJob)

const queuedJob: ProcessingJob = {
  id: 7,
  documentId: 42,
  status: 'queued',
  cancelable: true,
  attemptCount: 0,
  errorMessage: null,
  createdAt: new Date('2026-08-18T02:00:01Z'),
  updatedAt: new Date('2026-08-18T02:00:01Z'),
  startedAt: null,
  completedAt: null,
}

beforeEach(() => {
  getProcessingJobMock.mockReset()
  queueDocumentProcessingMock.mockReset()
  getLatestProcessingJobsMock.mockReset()
  cancelProcessingJobMock.mockReset()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('useProcessingJob', () => {
  it('从 queued 轮询到终态后停止，不产生额外请求', async () => {
    vi.useFakeTimers()
    const succeededJob: ProcessingJob = {
      ...queuedJob,
      status: 'succeeded',
      cancelable: false,
      attemptCount: 1,
      completedAt: new Date('2026-08-18T02:00:03Z'),
    }
    queueDocumentProcessingMock.mockResolvedValue(queuedJob)
    getProcessingJobMock.mockResolvedValue(succeededJob)

    const scope = effectScope()
    const processing = scope.run(() => useProcessingJob({ pollIntervalMs: 20 }))
    if (!processing) throw new Error('processing composable was not created')

    await processing.queue(42)
    expect(processing.state.value).toBe('queued')

    await vi.advanceTimersByTimeAsync(20)
    expect(getProcessingJobMock).toHaveBeenCalledTimes(1)
    expect(processing.state.value).toBe('succeeded')

    await vi.advanceTimersByTimeAsync(100)
    expect(getProcessingJobMock).toHaveBeenCalledTimes(1)
    scope.stop()
  })

  it('按文档恢复最近任务，并允许取消 queued 任务', async () => {
    const canceledJob: ProcessingJob = {
      ...queuedJob,
      status: 'canceled',
      cancelable: false,
      completedAt: new Date('2026-08-18T02:00:03Z'),
    }
    getLatestProcessingJobsMock.mockResolvedValue([{ documentId: 42, job: queuedJob }])
    cancelProcessingJobMock.mockResolvedValue(canceledJob)

    const scope = effectScope()
    const processing = scope.run(() => useProcessingJob({ pollIntervalMs: 20 }))
    if (!processing) throw new Error('processing composable was not created')

    await processing.discover(42)
    expect(processing.state.value).toBe('queued')
    expect(processing.canCancel.value).toBe(true)

    await processing.cancel()
    expect(cancelProcessingJobMock).toHaveBeenCalledWith(7, expect.any(AbortSignal))
    expect(processing.state.value).toBe('canceled')
    expect(processing.hasActiveJob.value).toBe(false)
    scope.stop()
  })

  it('取消与 Worker 领取冲突时回读 processing 真实状态', async () => {
    const processingJob: ProcessingJob = {
      ...queuedJob,
      status: 'processing',
      cancelable: false,
      startedAt: new Date('2026-08-18T02:00:02Z'),
    }
    queueDocumentProcessingMock.mockResolvedValue(queuedJob)
    cancelProcessingJobMock.mockRejectedValue(
      new ApiError('conflict', 'processing job cannot be canceled', {
        status: 409,
        code: 'processing_job_processing',
        requestId: 'cancel-race-1',
      }),
    )
    getProcessingJobMock.mockResolvedValue(processingJob)

    const scope = effectScope()
    const processing = scope.run(() => useProcessingJob())
    if (!processing) throw new Error('processing composable was not created')

    await processing.queue(42)
    await processing.cancel()

    expect(getProcessingJobMock).toHaveBeenCalledWith(7, expect.any(AbortSignal))
    expect(processing.state.value).toBe('processing')
    expect(processing.job.value?.cancelable).toBe(false)
    scope.stop()
  })

  it('解析容量拒绝期间阻止重复提交，到期后只开放手动重试', async () => {
    vi.useFakeTimers()
    queueDocumentProcessingMock
      .mockRejectedValueOnce(
        new ApiError('server', 'busy', {
          status: 503,
          code: 'processing_queue_capacity_exhausted',
          retryAfterSeconds: 5,
          requestId: 'processing-capacity-1',
        }),
      )
      .mockResolvedValueOnce(queuedJob)

    const scope = effectScope()
    const processing = scope.run(() => useProcessingJob())
    if (!processing) throw new Error('processing composable was not created')

    await processing.queue(42)
    expect(processing.state.value).toBe('capacity')
    expect(processing.retryAfterSeconds.value).toBe(5)
    await processing.queue(42)
    expect(queueDocumentProcessingMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5_000)
    expect(queueDocumentProcessingMock).toHaveBeenCalledTimes(1)
    await processing.queue(42)
    expect(queueDocumentProcessingMock).toHaveBeenCalledTimes(2)
    expect(processing.state.value).toBe('queued')
    scope.stop()
  })
})
