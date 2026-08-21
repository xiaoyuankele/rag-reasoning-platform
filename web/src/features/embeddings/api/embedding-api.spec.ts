import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'
import {
  getLatestEmbeddingJobs,
  mapEmbeddingBatchResponse,
  mapEmbeddingJobResponse,
  mapLatestEmbeddingJobsResponse,
  queueEmbeddingJob,
  queueEmbeddingJobs,
} from './embedding-api'

vi.mock('../../../shared/api/http-client', () => ({
  httpClient: { get: vi.fn(), post: vi.fn() },
}))

const postMock = vi.mocked(httpClient.post)

const jobDto = {
  id: 31,
  document_id: 42,
  model_name: 'text-embedding-v4',
  dimensions: 1536,
  status: 'queued',
  attempt_count: 0,
  error_message: null,
  next_attempt_at: '2026-08-21T02:00:00Z',
  prompt_tokens: null,
  total_tokens: null,
  created_at: '2026-08-21T02:00:00Z',
  updated_at: '2026-08-21T02:00:00Z',
  started_at: null,
  completed_at: null,
}

beforeEach(() => postMock.mockReset())

describe('embedding API response mapping', () => {
  it('映射完整任务 DTO 和可空计费字段', () => {
    const result = mapEmbeddingJobResponse(jobDto)

    expect(result).toMatchObject({
      id: 31,
      documentId: 42,
      modelName: 'text-embedding-v4',
      dimensions: 1536,
      status: 'queued',
      promptTokens: null,
    })
    expect(result.nextAttemptAt).toEqual(new Date('2026-08-21T02:00:00Z'))
  })

  it('拒绝未知任务状态和非法时间', () => {
    expect(() => mapEmbeddingJobResponse({ ...jobDto, status: 'paused' })).toThrow(
      new ApiError('invalid-response', '后端向量任务响应不符合约定。'),
    )
    expect(() => mapEmbeddingJobResponse({ ...jobDto, completed_at: 'not-a-date' })).toThrow(
      new ApiError('invalid-response', '后端向量任务响应不符合约定。'),
    )
  })

  it('映射批量创建、复用和逐项失败', () => {
    const result = mapEmbeddingBatchResponse({
      items: [
        { document_id: 42, outcome: 'created', job: jobDto },
        {
          document_id: 43,
          outcome: 'already_active',
          job: { ...jobDto, id: 32, document_id: 43, status: 'processing' },
        },
        {
          document_id: 99,
          outcome: 'not_found',
          error: { error: 'document not found', code: 'document_not_found' },
        },
      ],
    })

    expect(result).toHaveLength(3)
    expect(result[0]).toMatchObject({ documentId: 42, outcome: 'created' })
    expect(result[1]?.job?.status).toBe('processing')
    expect(result[2]).toEqual({
      documentId: 99,
      outcome: 'not_found',
      job: null,
      errorMessage: 'document not found',
      errorCode: 'document_not_found',
    })
  })

  it('拒绝成功项缺少任务或任务属于另一份文档', () => {
    expect(() =>
      mapEmbeddingBatchResponse({ items: [{ document_id: 42, outcome: 'created' }] }),
    ).toThrow(new ApiError('invalid-response', '后端批量向量任务响应不符合约定。'))

    expect(() =>
      mapEmbeddingBatchResponse({
        items: [{ document_id: 42, outcome: 'created', job: { ...jobDto, document_id: 43 } }],
      }),
    ).toThrow(new ApiError('invalid-response', '后端批量向量任务与文档不一致。'))
  })

  it('校验单篇任务与请求文档一致', async () => {
    postMock.mockResolvedValue({ status: 202, data: jobDto })
    await expect(queueEmbeddingJob(42)).resolves.toMatchObject({ created: true })

    postMock.mockResolvedValue({ status: 202, data: { ...jobDto, document_id: 43 } })
    await expect(queueEmbeddingJob(42)).rejects.toThrow(
      new ApiError('invalid-response', '后端向量任务与请求文档不一致。'),
    )
  })

  it('要求批量接口为每个请求文档逐项返回', async () => {
    postMock.mockResolvedValue({
      status: 200,
      data: { items: [{ document_id: 42, outcome: 'created', job: jobDto }] },
    })

    await expect(queueEmbeddingJobs([42, 43])).rejects.toThrow(
      new ApiError('invalid-response', '后端批量向量任务未逐项返回请求结果。'),
    )
  })

  it('映射最新任务与后端明确返回的无任务状态', async () => {
    const response = {
      items: [
        { document_id: 42, job: jobDto },
        { document_id: 43, job: null },
      ],
    }

    expect(mapLatestEmbeddingJobsResponse(response)).toEqual([
      { documentId: 42, job: expect.objectContaining({ id: 31, documentId: 42 }) },
      { documentId: 43, job: null },
    ])

    postMock.mockResolvedValue({ status: 200, data: response })
    await expect(getLatestEmbeddingJobs([42, 43])).resolves.toHaveLength(2)
    expect(postMock).toHaveBeenCalledWith(
      '/embedding-jobs/latest',
      { document_ids: [42, 43] },
      { signal: undefined },
    )
  })

  it('拒绝最新任务缺项、乱序或任务与文档不一致', async () => {
    expect(() =>
      mapLatestEmbeddingJobsResponse({
        items: [{ document_id: 42, job: { ...jobDto, document_id: 43 } }],
      }),
    ).toThrow(new ApiError('invalid-response', '后端最新向量任务与文档不一致。'))

    postMock.mockResolvedValue({
      status: 200,
      data: {
        items: [
          { document_id: 43, job: null },
          { document_id: 42, job: jobDto },
        ],
      },
    })
    await expect(getLatestEmbeddingJobs([42, 43])).rejects.toThrow(
      new ApiError('invalid-response', '后端最新向量任务未按请求顺序逐项返回。'),
    )
  })
})
