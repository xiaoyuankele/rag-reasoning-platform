import type {
  DocumentChunk,
  DocumentChunkPage,
} from '../../../entities/document/model/document-chunk'
import type { DocumentPagination } from '../../../entities/document/model/document'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

interface DocumentChunkDto {
  chunk_id: number
  chunk_index: number
  content: string
  page_start: number | null
  page_end: number | null
  created_at: string
}

interface PaginationDto {
  page: number
  page_size: number
  total: number
  total_pages: number
}

interface DocumentChunkPageDto {
  document_id: number
  chunks: DocumentChunkDto[]
  pagination: PaginationDto
}

export interface ListDocumentChunksParams {
  documentId: number
  page: number
  pageSize: number
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isInteger(value: unknown, minimum = 0): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum
}

function isNullablePositiveInteger(value: unknown): value is number | null {
  return value === null || isInteger(value, 1)
}

function isDateTime(value: unknown): value is string {
  return typeof value === 'string' && !Number.isNaN(Date.parse(value))
}

function isChunkDto(value: unknown): value is DocumentChunkDto {
  if (!isRecord(value)) return false

  const hasValidPages =
    (value.page_start === null && value.page_end === null) ||
    (isInteger(value.page_start, 1) &&
      isInteger(value.page_end, 1) &&
      value.page_end >= value.page_start)

  return (
    isInteger(value.chunk_id, 1) &&
    isInteger(value.chunk_index) &&
    typeof value.content === 'string' &&
    isNullablePositiveInteger(value.page_start) &&
    isNullablePositiveInteger(value.page_end) &&
    hasValidPages &&
    isDateTime(value.created_at)
  )
}

function isPaginationDto(value: unknown): value is PaginationDto {
  if (!isRecord(value)) return false
  return (
    isInteger(value.page, 1) &&
    isInteger(value.page_size, 1) &&
    isInteger(value.total) &&
    isInteger(value.total_pages)
  )
}

function mapChunk(source: DocumentChunkDto): DocumentChunk {
  return {
    id: source.chunk_id,
    index: source.chunk_index,
    content: source.content,
    pageStart: source.page_start,
    pageEnd: source.page_end,
    createdAt: new Date(source.created_at),
  }
}

function mapPagination(source: PaginationDto): DocumentPagination {
  return {
    page: source.page,
    pageSize: source.page_size,
    total: source.total,
    totalPages: source.total_pages,
  }
}

/** 校验 chunks 分页响应，并保留后端给出的原文顺序。 */
export function mapDocumentChunkPageResponse(data: unknown): DocumentChunkPage {
  if (!isRecord(data)) {
    throw new ApiError('invalid-response', '后端文本块响应不符合约定。')
  }

  const source = data as unknown as DocumentChunkPageDto
  const chunksAreOrdered =
    Array.isArray(source.chunks) &&
    source.chunks.every(
      (chunk, index) => index === 0 || chunk.chunk_index > source.chunks[index - 1]!.chunk_index,
    )
  if (
    !isInteger(source.document_id, 1) ||
    !Array.isArray(source.chunks) ||
    !source.chunks.every(isChunkDto) ||
    !chunksAreOrdered ||
    !isPaginationDto(source.pagination)
  ) {
    throw new ApiError('invalid-response', '后端文本块响应不符合约定。')
  }

  return {
    documentId: source.document_id,
    chunks: source.chunks.map(mapChunk),
    pagination: mapPagination(source.pagination),
  }
}

export async function listDocumentChunks(
  params: ListDocumentChunksParams,
  signal?: AbortSignal,
): Promise<DocumentChunkPage> {
  const response = await httpClient.get<unknown>(`/documents/${params.documentId}/chunks`, {
    params: { page: params.page, page_size: params.pageSize },
    signal,
  })
  const result = mapDocumentChunkPageResponse(response.data)
  if (result.documentId !== params.documentId) {
    throw new ApiError('invalid-response', '后端文本块与当前文档不一致。')
  }
  return result
}
