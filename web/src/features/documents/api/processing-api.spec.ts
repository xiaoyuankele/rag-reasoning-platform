import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'
import {
  cancelProcessingJob,
  getLatestProcessingJobs,
  mapLatestProcessingJobsResponse,
  mapProcessingJobResponse,
} from './processing-api'

vi.mock('../../../shared/api/http-client', () => ({
  httpClient: { get: vi.fn(), post: vi.fn() },
}))

const postMock = vi.mocked(httpClient.post)

const processingJobDto = {
  id: 7,
  document_id: 42,
  status: 'processing',
  cancelable: false,
  attempt_count: 1,
  error_message: null,
  created_at: '2026-08-18T02:00:00Z',
  updated_at: '2026-08-18T02:00:02Z',
  started_at: '2026-08-18T02:00:01Z',
  completed_at: null,
}

beforeEach(() => postMock.mockReset())

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

  it('接受 canceled 终态并保留后端 cancelable 事实', () => {
    const result = mapProcessingJobResponse({
      ...processingJobDto,
      status: 'canceled',
      cancelable: false,
      completed_at: '2026-08-18T02:00:04Z',
    })

    expect(result.status).toBe('canceled')
    expect(result.cancelable).toBe(false)
  })

  it('拒绝与状态矛盾的 cancelable', () => {
    expect(() =>
      mapProcessingJobResponse({ ...processingJobDto, status: 'queued', cancelable: false }),
    ).toThrow(new ApiError('invalid-response', '后端解析任务响应不符合约定。'))
  })

  it('按文档转换最新任务与 null，并拒绝任务错配', () => {
    const queuedDto = { ...processingJobDto, status: 'queued', cancelable: true }
    expect(
      mapLatestProcessingJobsResponse({
        items: [
          { document_id: 42, job: queuedDto },
          { document_id: 43, job: null },
        ],
      }),
    ).toMatchObject([
      { documentId: 42, job: { id: 7, status: 'queued', cancelable: true } },
      { documentId: 43, job: null },
    ])

    expect(() =>
      mapLatestProcessingJobsResponse({
        items: [{ document_id: 99, job: queuedDto }],
      }),
    ).toThrow(new ApiError('invalid-response', '后端最新解析任务与文档不一致。'))
  })

  it('按首次顺序请求最新任务，并校验逐项完整返回', async () => {
    postMock.mockResolvedValue({
      status: 200,
      data: {
        items: [
          { document_id: 42, job: null },
          { document_id: 43, job: null },
        ],
      },
    })

    await expect(getLatestProcessingJobs([42, 43, 42])).resolves.toHaveLength(2)
    expect(postMock).toHaveBeenCalledWith(
      '/processing-jobs/latest',
      { document_ids: [42, 43] },
      { signal: undefined },
    )

    postMock.mockResolvedValue({
      status: 200,
      data: { items: [{ document_id: 43, job: null }] },
    })
    await expect(getLatestProcessingJobs([42, 43])).rejects.toThrow(
      new ApiError('invalid-response', '后端最新解析任务未按请求顺序逐项返回。'),
    )
  })

  it('取消接口只接受与请求 ID 一致的任务', async () => {
    const canceledDto = {
      ...processingJobDto,
      status: 'canceled',
      cancelable: false,
      completed_at: '2026-08-18T02:00:04Z',
    }
    postMock.mockResolvedValue({ status: 200, data: canceledDto })
    await expect(cancelProcessingJob(7)).resolves.toMatchObject({ id: 7, status: 'canceled' })

    postMock.mockResolvedValue({ status: 200, data: { ...canceledDto, id: 8 } })
    await expect(cancelProcessingJob(7)).rejects.toThrow(
      new ApiError('invalid-response', '后端取消解析任务响应与请求 ID 不一致。'),
    )
  })
})
