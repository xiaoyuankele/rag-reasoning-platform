import type {
  SemanticSearchHit,
  SemanticSearchResult,
} from '../../../entities/search-result/model/search-result'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

export interface SemanticSearchParams {
  query: string
  documentId?: number
  topK: number
}

interface SemanticSearchHitDto {
  chunk_id: number
  document_id: number
  chunk_index: number
  title: string | null
  original_name: string
  mime_type: string
  content: string
  page_start: number | null
  page_end: number | null
  similarity: number
}

interface SemanticSearchResponseDto {
  query: string
  hits: SemanticSearchHitDto[]
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

function isSemanticSearchHitDto(value: unknown): value is SemanticSearchHitDto {
  if (!isRecord(value)) return false

  return (
    isInteger(value.chunk_id, 1) &&
    isInteger(value.document_id, 1) &&
    isInteger(value.chunk_index) &&
    (value.title === null || typeof value.title === 'string') &&
    typeof value.original_name === 'string' &&
    value.original_name.trim().length > 0 &&
    typeof value.mime_type === 'string' &&
    value.mime_type.trim().length > 0 &&
    typeof value.content === 'string' &&
    isNullablePositiveInteger(value.page_start) &&
    isNullablePositiveInteger(value.page_end) &&
    typeof value.similarity === 'number' &&
    Number.isFinite(value.similarity) &&
    value.similarity >= -1 &&
    value.similarity <= 1
  )
}

function isSemanticSearchResponseDto(value: unknown): value is SemanticSearchResponseDto {
  return (
    isRecord(value) &&
    typeof value.query === 'string' &&
    value.query.trim().length > 0 &&
    Array.isArray(value.hits) &&
    value.hits.every(isSemanticSearchHitDto)
  )
}

function mapSemanticSearchHit(source: SemanticSearchHitDto): SemanticSearchHit {
  return {
    chunkId: source.chunk_id,
    documentId: source.document_id,
    chunkIndex: source.chunk_index,
    title: source.title,
    originalName: source.original_name,
    mimeType: source.mime_type,
    content: source.content,
    pageStart: source.page_start,
    pageEnd: source.page_end,
    similarity: source.similarity,
  }
}

/** 运行时校验语义检索 DTO，避免未知字段类型直接进入结果卡片。 */
export function mapSemanticSearchResponse(data: unknown): SemanticSearchResult {
  if (!isSemanticSearchResponseDto(data)) {
    throw new ApiError('invalid-response', '后端语义检索响应不符合约定。')
  }

  return {
    query: data.query,
    hits: data.hits.map(mapSemanticSearchHit),
  }
}

/** 显式调用可能产生远程 Embedding 费用的 POST /semantic-search。 */
export async function searchSemantically(
  params: SemanticSearchParams,
  signal?: AbortSignal,
): Promise<SemanticSearchResult> {
  const response = await httpClient.post<unknown>(
    '/semantic-search',
    {
      query: params.query,
      document_id: params.documentId,
      top_k: params.topK,
    },
    {
      signal,
      timeout: 30_000,
    },
  )

  return mapSemanticSearchResponse(response.data)
}
