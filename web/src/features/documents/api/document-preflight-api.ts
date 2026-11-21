import type { ResearchDocument } from '../../../entities/document/model/document'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'
import { mapDocumentResponse } from './document-api'

export interface DocumentPreflightInput {
  sha256: string
  sizeBytes: number
}

export interface DocumentPreflightResult {
  exists: boolean
  document: ResearchDocument | null
}

interface DocumentPreflightResponseDto {
  exists: boolean
  document: unknown
}

const lowercaseSha256Pattern = /^[0-9a-f]{64}$/

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function assertValidInput(input: DocumentPreflightInput): void {
  if (
    !lowercaseSha256Pattern.test(input.sha256) ||
    !Number.isSafeInteger(input.sizeBytes) ||
    input.sizeBytes <= 0
  ) {
    throw new ApiError('client', '本地文件预检参数不符合约定。')
  }
}

/**
 * 校验预检响应的真假分支，并确认命中的文档确实对应本次摘要与文件大小。
 * 这不会把客户端摘要变成系统真相；实际上传仍由后端重新计算摘要。
 */
export function mapDocumentPreflightResponse(
  data: unknown,
  expected: DocumentPreflightInput,
): DocumentPreflightResult {
  if (!isRecord(data) || typeof data.exists !== 'boolean' || !('document' in data)) {
    throw new ApiError('invalid-response', '后端文件预检响应不符合约定。')
  }

  const source = data as unknown as DocumentPreflightResponseDto
  if (!source.exists) {
    if (source.document !== null) {
      throw new ApiError('invalid-response', '后端文件预检结果与文档摘要不一致。')
    }
    return { exists: false, document: null }
  }

  if (source.document === null) {
    throw new ApiError('invalid-response', '后端文件预检结果缺少已有文档。')
  }

  const document = mapDocumentResponse(source.document)
  if (document.sha256 !== expected.sha256 || document.sizeBytes !== expected.sizeBytes) {
    throw new ApiError('invalid-response', '后端文件预检命中了不匹配的文档。')
  }

  return { exists: true, document }
}

/** 使用当前 Session 在上传正文前检查同用户是否已有完全相同的文件。 */
export async function preflightDocument(
  input: DocumentPreflightInput,
  signal?: AbortSignal,
): Promise<DocumentPreflightResult> {
  assertValidInput(input)

  const response = await httpClient.post<unknown>(
    '/documents/preflight',
    {
      sha256: input.sha256,
      size_bytes: input.sizeBytes,
    },
    { signal },
  )

  if (response.status !== 200) {
    throw new ApiError('invalid-response', '后端文件预检状态不符合约定。')
  }

  return mapDocumentPreflightResponse(response.data, input)
}
