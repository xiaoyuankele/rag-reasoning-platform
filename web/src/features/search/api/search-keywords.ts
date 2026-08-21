import type {
  KeywordSearchHit,
  KeywordSearchOperator,
  KeywordSearchPage,
  KeywordSearchPagination,
  KeywordSearchWithin,
} from '../../../entities/search-result/model/search-result'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

interface KeywordSearchBaseParams {
  documentId?: number
  page: number
  pageSize: number
}

export interface KeywordPhraseSearchParams extends KeywordSearchBaseParams {
  mode: 'phrase'
  query: string
}

export interface KeywordTermsSearchParams extends KeywordSearchBaseParams {
  mode: 'terms'
  terms: string[]
  operator: KeywordSearchOperator
  within: KeywordSearchWithin
}

/** 短语模式与多关键词模式互斥，避免同时发送 q 和 term。 */
export type KeywordSearchParams = KeywordPhraseSearchParams | KeywordTermsSearchParams

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
  terms?: string[]
  operator?: KeywordSearchOperator
  within?: KeywordSearchWithin
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

  if (
    typeof value.query !== 'string' ||
    !Array.isArray(value.results) ||
    !value.results.every(isSearchHitDto) ||
    !isSearchPaginationDto(value.pagination)
  ) {
    return false
  }

  if (value.terms === undefined) {
    return (
      value.query.trim().length > 0 && value.operator === undefined && value.within === undefined
    )
  }

  if (
    !Array.isArray(value.terms) ||
    value.terms.length < 2 ||
    value.terms.length > 8 ||
    !value.terms.every(
      (term) => typeof term === 'string' && term.trim().length > 0 && [...term].length <= 100,
    ) ||
    (value.operator !== 'all' && value.operator !== 'any') ||
    value.within !== 'chunk'
  ) {
    return false
  }

  const normalizedTerms = new Set(
    value.terms.map((term) => (term as string).trim().toLocaleLowerCase()),
  )
  const totalCharacters = value.terms.reduce(
    (total, term) => total + [...(term as string)].length,
    0,
  )
  return normalizedTerms.size === value.terms.length && totalCharacters <= 200
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
    terms: data.terms ?? [],
    operator: data.operator ?? null,
    within: data.within ?? null,
    results: data.results.map(mapSearchHit),
    pagination: mapPagination(data.pagination),
  }
}

/** 调用无远程模型费用的 GET /search 关键词检索接口。 */
export async function searchKeywords(
  params: KeywordSearchParams,
  signal?: AbortSignal,
): Promise<KeywordSearchPage> {
  const searchParams =
    params.mode === 'phrase'
      ? { q: params.query }
      : {
          term: params.terms,
          operator: params.operator,
          within: params.within,
        }
  const response = await httpClient.get<unknown>('/search', {
    params: {
      ...searchParams,
      document_id: params.documentId,
      page: params.page,
      page_size: params.pageSize,
    },
    // Axios 使用 term=a&term=b，匹配 Gin GetQueryArray("term")，不发送 term[]=a。
    paramsSerializer: { indexes: null },
    signal,
  })

  return mapKeywordSearchResponse(response.data)
}
