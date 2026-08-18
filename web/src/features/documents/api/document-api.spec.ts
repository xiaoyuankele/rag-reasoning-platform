import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { mapDocumentListResponse, mapDocumentUploadResponse } from './document-api'

const documentDto = {
  id: 42,
  title: 'Maglev control study',
  original_name: 'maglev.pdf',
  mime_type: 'application/pdf',
  size_bytes: 2048,
  sha256: 'a'.repeat(64),
  status: 'uploaded',
  error_message: null,
  created_at: '2026-08-18T02:00:00Z',
  updated_at: '2026-08-18T02:00:00Z',
}

describe('document API response mapping', () => {
  it('把分页列表 DTO 转换为前端文档模型', () => {
    const result = mapDocumentListResponse({
      documents: [documentDto],
      pagination: {
        page: 1,
        page_size: 20,
        total: 1,
        total_pages: 1,
      },
    })

    expect(result.documents[0]).toMatchObject({
      id: 42,
      title: 'Maglev control study',
      originalName: 'maglev.pdf',
      mimeType: 'application/pdf',
      sizeBytes: 2048,
      status: 'uploaded',
    })
    expect(result.documents[0]?.createdAt).toEqual(new Date('2026-08-18T02:00:00Z'))
    expect(result.pagination).toEqual({ page: 1, pageSize: 20, total: 1, totalPages: 1 })
  })

  it('把 200 + duplicate:true 映射为已有文档成功结果', () => {
    const result = mapDocumentUploadResponse({ ...documentDto, duplicate: true }, 200)

    expect(result.duplicate).toBe(true)
    expect(result.document.id).toBe(42)
    expect(result.document.originalName).toBe('maglev.pdf')
  })

  it('拒绝 HTTP 状态与 duplicate 字段不一致的响应', () => {
    expect(() => mapDocumentUploadResponse({ ...documentDto, duplicate: true }, 201)).toThrow(
      new ApiError('invalid-response', '后端文档上传状态与响应不一致。'),
    )
  })

  it('拒绝缺少可信哈希的异常文档 DTO', () => {
    expect(() =>
      mapDocumentListResponse({
        documents: [{ ...documentDto, sha256: 'not-a-sha256' }],
        pagination: { page: 1, page_size: 20, total: 1, total_pages: 1 },
      }),
    ).toThrow(new ApiError('invalid-response', '后端文档列表响应不符合约定。'))
  })
})
