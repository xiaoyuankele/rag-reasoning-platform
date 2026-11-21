import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { mapDocumentPreflightResponse } from './document-preflight-api'

const expected = {
  sha256: 'a'.repeat(64),
  sizeBytes: 2048,
}

const documentDto = {
  id: 42,
  title: null,
  original_name: 'first-upload.pdf',
  mime_type: 'application/pdf',
  size_bytes: expected.sizeBytes,
  sha256: expected.sha256,
  status: 'ready',
  error_message: null,
  created_at: '2026-08-20T02:00:00Z',
  updated_at: '2026-08-20T02:01:00Z',
}

describe('document preflight API response mapping', () => {
  it('映射不存在结果', () => {
    expect(mapDocumentPreflightResponse({ exists: false, document: null }, expected)).toEqual({
      exists: false,
      document: null,
    })
  })

  it('映射当前用户已有文档', () => {
    const result = mapDocumentPreflightResponse({ exists: true, document: documentDto }, expected)

    expect(result.exists).toBe(true)
    expect(result.document).toMatchObject({
      id: 42,
      originalName: 'first-upload.pdf',
      sha256: expected.sha256,
      sizeBytes: expected.sizeBytes,
      status: 'ready',
    })
  })

  it('拒绝 exists=false 却返回文档的矛盾响应', () => {
    expect(() =>
      mapDocumentPreflightResponse({ exists: false, document: documentDto }, expected),
    ).toThrow(new ApiError('invalid-response', '后端文件预检结果与文档摘要不一致。'))
  })

  it('拒绝命中文档摘要或大小不匹配', () => {
    expect(() =>
      mapDocumentPreflightResponse(
        { exists: true, document: { ...documentDto, size_bytes: 2049 } },
        expected,
      ),
    ).toThrow(new ApiError('invalid-response', '后端文件预检命中了不匹配的文档。'))
  })
})
