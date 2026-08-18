import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ProcessingJob } from '../../../entities/processing-job/model/processing-job'
import { getProcessingJob, queueDocumentProcessing } from '../api/processing-api'
import { useProcessingJob } from './use-processing-job'

vi.mock('../api/processing-api', () => ({
  getProcessingJob: vi.fn(),
  queueDocumentProcessing: vi.fn(),
}))

const getProcessingJobMock = vi.mocked(getProcessingJob)
const queueDocumentProcessingMock = vi.mocked(queueDocumentProcessing)

const queuedJob: ProcessingJob = {
  id: 7,
  documentId: 42,
  status: 'queued',
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
})
