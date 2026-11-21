import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'
import { mapKeywordSearchResponse, searchKeywords } from './search-keywords'

vi.mock('../../../shared/api/http-client', () => ({
  httpClient: { get: vi.fn() },
}))

const getMock = vi.mocked(httpClient.get)

beforeEach(() => getMock.mockReset())

describe('mapKeywordSearchResponse', () => {
  it('把后端 snake_case DTO 转换为前端搜索模型', () => {
    const result = mapKeywordSearchResponse({
      query: 'bridge',
      results: [
        {
          chunk_id: 11,
          document_id: 7,
          chunk_index: 2,
          title: 'Bridge vibration study',
          original_name: 'bridge-study.pdf',
          mime_type: 'application/pdf',
          content: 'bridge vibration analysis',
          page_start: 3,
          page_end: 4,
        },
      ],
      pagination: {
        page: 1,
        page_size: 10,
        total: 1,
        total_pages: 1,
      },
    })

    expect(result).toEqual({
      query: 'bridge',
      terms: [],
      operator: null,
      within: null,
      results: [
        {
          chunkId: 11,
          documentId: 7,
          chunkIndex: 2,
          title: 'Bridge vibration study',
          originalName: 'bridge-study.pdf',
          mimeType: 'application/pdf',
          content: 'bridge vibration analysis',
          pageStart: 3,
          pageEnd: 4,
        },
      ],
      pagination: {
        page: 1,
        pageSize: 10,
        total: 1,
        totalPages: 1,
      },
    })
  })

  it('拒绝缺少分页字段的异常响应', () => {
    expect(() =>
      mapKeywordSearchResponse({
        query: 'bridge',
        results: [],
      }),
    ).toThrow(new ApiError('invalid-response', '后端检索响应不符合约定。'))
  })

  it('校验多关键词响应并使用重复 term 查询参数', async () => {
    const response = {
      query: '',
      terms: ['磁悬浮', '振动'],
      operator: 'all',
      within: 'chunk',
      results: [],
      pagination: {
        page: 1,
        page_size: 10,
        total: 0,
        total_pages: 0,
      },
    }
    getMock.mockResolvedValue({ data: response })

    await expect(
      searchKeywords({
        mode: 'terms',
        terms: ['磁悬浮', '振动'],
        operator: 'all',
        within: 'chunk',
        documentId: 7,
        page: 1,
        pageSize: 10,
      }),
    ).resolves.toMatchObject({
      terms: ['磁悬浮', '振动'],
      operator: 'all',
      within: 'chunk',
    })
    expect(getMock).toHaveBeenCalledWith(
      '/search',
      expect.objectContaining({
        params: {
          term: ['磁悬浮', '振动'],
          operator: 'all',
          within: 'chunk',
          document_id: 7,
          page: 1,
          page_size: 10,
        },
        paramsSerializer: { indexes: null },
      }),
    )
  })

  it('拒绝多关键词响应缺少 operator 或使用未知 within', () => {
    const baseResponse = {
      query: '',
      terms: ['磁悬浮', '振动'],
      operator: 'all',
      within: 'chunk',
      results: [],
      pagination: { page: 1, page_size: 10, total: 0, total_pages: 0 },
    }
    expect(() => mapKeywordSearchResponse({ ...baseResponse, operator: undefined })).toThrow(
      new ApiError('invalid-response', '后端检索响应不符合约定。'),
    )
    expect(() => mapKeywordSearchResponse({ ...baseResponse, within: 'sentence' })).toThrow(
      new ApiError('invalid-response', '后端检索响应不符合约定。'),
    )
  })
})
