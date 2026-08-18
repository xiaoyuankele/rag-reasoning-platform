import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { mapDocumentChunkPageResponse } from './document-chunk-api'

const chunkPageDto = {
  document_id: 42,
  chunks: [
    {
      chunk_id: 5,
      chunk_index: 0,
      content: 'A stable chunk of parsed text.',
      page_start: 1,
      page_end: 2,
      created_at: '2026-08-18T02:00:03Z',
    },
  ],
  pagination: { page: 1, page_size: 10, total: 1, total_pages: 1 },
}

describe('document chunk API response mapping', () => {
  it('映射文本块、页码和分页信息', () => {
    const result = mapDocumentChunkPageResponse(chunkPageDto)

    expect(result.documentId).toBe(42)
    expect(result.chunks[0]).toMatchObject({
      id: 5,
      index: 0,
      content: 'A stable chunk of parsed text.',
      pageStart: 1,
      pageEnd: 2,
    })
    expect(result.pagination).toEqual({ page: 1, pageSize: 10, total: 1, totalPages: 1 })
  })

  it('拒绝只有起始页或倒序页码的异常文本块', () => {
    const invalidChunk = { ...chunkPageDto.chunks[0], page_start: 3, page_end: 2 }

    expect(() => mapDocumentChunkPageResponse({ ...chunkPageDto, chunks: [invalidChunk] })).toThrow(
      new ApiError('invalid-response', '后端文本块响应不符合约定。'),
    )
  })

  it('拒绝打乱原文顺序的文本块响应', () => {
    const laterChunk = { ...chunkPageDto.chunks[0], chunk_id: 6, chunk_index: 2 }
    const earlierChunk = { ...chunkPageDto.chunks[0], chunk_id: 7, chunk_index: 1 }

    expect(() =>
      mapDocumentChunkPageResponse({ ...chunkPageDto, chunks: [laterChunk, earlierChunk] }),
    ).toThrow(new ApiError('invalid-response', '后端文本块响应不符合约定。'))
  })
})
