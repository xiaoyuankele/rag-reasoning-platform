import type {
  KeywordSearchHit,
  KeywordSearchPage,
  KeywordSearchPagination,
} from '../../../entities/search-result/model/search-result'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

/** 关键词检索请求参数；page/pageSize 始终由前端提供明确值。 */
export interface KeywordSearchParams {
  query: string
  documentId?: number
  page: number
  pageSize: number
}

interface SearchHitDto {
  chunk_id: number
  document_id: number
  chunk_index: number
  title: string | null
  original_name: string
  mime_type: string
  content: string
  page_start: number | null
  page_end: number | null
}

interface SearchPaginationDto {
  page: number
  page_size: number
  total: number
  total_pages: number
}

interface SearchResponseDto {
  query: string
  results: SearchHitDto[]
  pagination: SearchPaginationDto
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

function isSearchHitDto(value: unknown): value is SearchHitDto {
  if (!isRecord(value)) return false

  return (
    isInteger(value.chunk_id, 1) &&
    isInteger(value.document_id, 1) &&
    isInteger(value.chunk_index) &&
    (typeof value.title === 'string' || value.title === null) &&
    typeof value.original_name === 'string' &&
    typeof value.mime_type === 'string' &&
    typeof value.content === 'string' &&
    isNullablePositiveInteger(value.page_start) &&
    isNullablePositiveInteger(value.page_end)
  )
}

function isSearchPaginationDto(value: unknown): value is SearchPaginationDto {
  if (!isRecord(value)) return false

  return (
    isInteger(value.page, 1) &&
    isInteger(value.page_size, 1) &&
    isInteger(value.total) &&
    isInteger(value.total_pages)
  )
}

function isSearchResponseDto(value: unknown): value is SearchResponseDto {
  if (!isRecord(value)) return false

  return (
    typeof value.query === 'string' &&
    Array.isArray(value.results) &&
    value.results.every(isSearchHitDto) &&
    isSearchPaginationDto(value.pagination)
  )
}

function mapSearchHit(source: SearchHitDto): KeywordSearchHit {
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
  }
}

function mapPagination(source: SearchPaginationDto): KeywordSearchPagination {
  return {
    page: source.page,
    pageSize: source.page_size,
    total: source.total,
    totalPages: source.total_pages,
  }
}

/** 校验后端搜索 DTO，并转换为组件使用的 camelCase 视图模型。 */
export function mapKeywordSearchResponse(data: unknown): KeywordSearchPage {
  if (!isSearchResponseDto(data)) {
    throw new ApiError('invalid-response', '后端检索响应不符合约定。')
  }

  return {
    query: data.query,
    results: data.results.map(mapSearchHit),
    pagination: mapPagination(data.pagination),
  }
}

/** 调用无远程模型费用的 GET /search 关键词检索接口。 */
export async function searchKeywords(
  params: KeywordSearchParams,
  signal?: AbortSignal,
): Promise<KeywordSearchPage> {
  const response = await httpClient.get<unknown>('/search', {
    params: {
      q: params.query,
      document_id: params.documentId,
      page: params.page,
      page_size: params.pageSize,
    },
    signal,
  })

  return mapKeywordSearchResponse(response.data)
}
