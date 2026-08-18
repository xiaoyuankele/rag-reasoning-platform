import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { mapProcessingJobResponse } from './processing-api'

const processingJobDto = {
  id: 7,
  document_id: 42,
  status: 'processing',
  attempt_count: 1,
  error_message: null,
  created_at: '2026-08-18T02:00:00Z',
  updated_at: '2026-08-18T02:00:02Z',
  started_at: '2026-08-18T02:00:01Z',
  completed_at: null,
}

describe('processing API response mapping', () => {
  it('隔离任务 DTO 的 snake_case 和可空时间', () => {
    const result = mapProcessingJobResponse(processingJobDto)

    expect(result).toMatchObject({
      id: 7,
      documentId: 42,
      status: 'processing',
      attemptCount: 1,
      completedAt: null,
    })
    expect(result.startedAt).toEqual(new Date('2026-08-18T02:00:01Z'))
  })

  it('拒绝后端新增但前端尚未认识的任务状态', () => {
    expect(() => mapProcessingJobResponse({ ...processingJobDto, status: 'cancelled' })).toThrow(
      new ApiError('invalid-response', '后端解析任务响应不符合约定。'),
    )
  })
})
