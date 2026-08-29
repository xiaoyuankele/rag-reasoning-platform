import { describe, expect, it } from 'vitest'
import { ApiError } from './api-error'
import { capacityFailureFromApiError, createCapacityFailure } from './capacity-error'

describe('capacity error presentation', () => {
  it.each([
    'upload_owner_concurrency_exhausted',
    'upload_capacity_exhausted',
    'processing_owner_active_job_limit',
    'processing_queue_capacity_exhausted',
    'embedding_owner_active_job_limit',
    'embedding_queue_capacity_exhausted',
    'embedding_provider_capacity_exhausted',
    'answer_capacity_exhausted',
  ])('识别正式容量 code：%s', (code) => {
    expect(createCapacityFailure(code, 5, 'capacity-1', 2)).toMatchObject({
      code,
      retryAfterSeconds: 5,
      requestId: 'capacity-1',
    })
  })

  it('不把普通 503 当作正式容量错误', () => {
    expect(createCapacityFailure('internal_error', 2, undefined, 5)).toBeNull()
  })

  it('后端意外漏发 Retry-After 时使用有限的保守等待', () => {
    const error = new ApiError('server', 'busy', {
      status: 503,
      code: 'answer_capacity_exhausted',
      requestId: 'answer-capacity-1',
    })

    expect(capacityFailureFromApiError(error, 2)).toMatchObject({
      code: 'answer_capacity_exhausted',
      retryAfterSeconds: 2,
      requestId: 'answer-capacity-1',
    })
  })
})
