import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'
import { mapSemanticSearchResponse, searchSemantically } from './search-semantically'

vi.mock('../../../shared/api/http-client', () => ({
  httpClient: { post: vi.fn() },
}))

const postMock = vi.mocked(httpClient.post)

const responseDto = {
  query: 'maglev vibration control',
  hits: [
    {
      chunk_id: 11,
      document_id: 7,
      chunk_index: 2,
      title: 'Maglev stability study',
      original_name: 'maglev.pdf',
      mime_type: 'application/pdf',
      content: 'Feedback control can improve suspension stability.',
      page_start: 3,
      page_end: 4,
      similarity: 0.91,
    },
  ],
}

beforeEach(() => postMock.mockReset())

describe('semantic search API', () => {
  it('校验并映射语义检索命中', () => {
    expect(mapSemanticSearchResponse(responseDto)).toEqual({
      query: 'maglev vibration control',
      hits: [
        {
          chunkId: 11,
          documentId: 7,
          chunkIndex: 2,
          title: 'Maglev stability study',
          originalName: 'maglev.pdf',
          mimeType: 'application/pdf',
          content: 'Feedback control can improve suspension stability.',
          pageStart: 3,
          pageEnd: 4,
          similarity: 0.91,
        },
      ],
    })
  })

  it('拒绝越界相似度和缺少查询的异常响应', () => {
    expect(() =>
      mapSemanticSearchResponse({
        ...responseDto,
        hits: [{ ...responseDto.hits[0], similarity: 1.1 }],
      }),
    ).toThrow(new ApiError('invalid-response', '后端语义检索响应不符合约定。'))
    expect(() => mapSemanticSearchResponse({ query: '', hits: [] })).toThrow(
      new ApiError('invalid-response', '后端语义检索响应不符合约定。'),
    )
  })

  it('使用 JSON 显式提交范围和 top_k', async () => {
    postMock.mockResolvedValue({ data: responseDto })

    await expect(
      searchSemantically({ query: 'maglev vibration control', documentId: 7, topK: 5 }),
    ).resolves.toMatchObject({ query: 'maglev vibration control' })
    expect(postMock).toHaveBeenCalledWith(
      '/semantic-search',
      {
        query: 'maglev vibration control',
        document_id: 7,
        top_k: 5,
      },
      { signal: undefined, timeout: 30_000 },
    )
  })
})
