import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { mapKeywordSearchResponse } from './search-keywords'

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
})
