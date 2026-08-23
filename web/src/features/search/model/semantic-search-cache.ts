import type {
  SemanticSearchHit,
  SemanticSearchResult,
} from '../../../entities/search-result/model/search-result'
import { privateSessionStorageKey } from '../../../shared/storage/private-session-storage'
import type { SemanticSearchParams } from '../api/search-semantically'

const cacheVersion = 2
const semanticCacheName = `semantic-search:last-result:v${cacheVersion}`
const semanticCacheLifetimeMs = 30 * 60 * 1_000
const legacyCacheKeys = [
  'rag-workspace:keyword-search:last-result',
  'rag-workspace:semantic-search:last-result',
]

interface CachedSemanticSearchEntry {
  version: typeof cacheVersion
  ownerUserId: number
  retainedWithUserConsent: true
  expiresAt: number
  params: {
    query: string
    documentId: number | null
    topK: number
  }
  result: SemanticSearchResult
}

export interface RestoredSemanticSearch {
  params: SemanticSearchParams
  result: SemanticSearchResult
}

/** 删除调试版本曾写入、没有用户隔离的永久缓存。 */
export function removeLegacySearchCaches(): void {
  try {
    for (const key of legacyCacheKeys) localStorage.removeItem(key)
  } catch {
    // 存储不可用时不阻塞检索页面。
  }

  try {
    const legacySessionKey = /^rag-workspace:user:\d+:semantic-search:last-result:v1$/u
    const keys = Array.from({ length: sessionStorage.length }, (_, index) =>
      sessionStorage.key(index),
    ).filter((key): key is string => key !== null && legacySessionKey.test(key))
    for (const key of keys) sessionStorage.removeItem(key)
  } catch {
    // 存储不可用时不阻塞检索页面。
  }
}

function cacheKey(ownerUserId: number): string {
  return privateSessionStorageKey(ownerUserId, semanticCacheName)
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

function isSemanticSearchHit(value: unknown): value is SemanticSearchHit {
  if (!isRecord(value)) return false

  return (
    isInteger(value.chunkId, 1) &&
    isInteger(value.documentId, 1) &&
    isInteger(value.chunkIndex) &&
    (value.title === null || typeof value.title === 'string') &&
    typeof value.originalName === 'string' &&
    value.originalName.trim().length > 0 &&
    typeof value.mimeType === 'string' &&
    value.mimeType.trim().length > 0 &&
    typeof value.content === 'string' &&
    isNullablePositiveInteger(value.pageStart) &&
    isNullablePositiveInteger(value.pageEnd) &&
    typeof value.similarity === 'number' &&
    Number.isFinite(value.similarity) &&
    value.similarity >= -1 &&
    value.similarity <= 1
  )
}

function isSemanticSearchResult(value: unknown): value is SemanticSearchResult {
  return (
    isRecord(value) &&
    typeof value.query === 'string' &&
    value.query.trim().length > 0 &&
    [...value.query].length <= 1_000 &&
    Array.isArray(value.hits) &&
    value.hits.every(isSemanticSearchHit)
  )
}

function isCachedSemanticSearchEntry(value: unknown): value is CachedSemanticSearchEntry {
  if (!isRecord(value) || !isRecord(value.params)) return false

  return (
    value.version === cacheVersion &&
    isInteger(value.ownerUserId, 1) &&
    value.retainedWithUserConsent === true &&
    typeof value.expiresAt === 'number' &&
    Number.isFinite(value.expiresAt) &&
    typeof value.params.query === 'string' &&
    value.params.query.trim().length > 0 &&
    [...value.params.query].length <= 1_000 &&
    isNullablePositiveInteger(value.params.documentId) &&
    isInteger(value.params.topK, 1) &&
    value.params.topK <= 20 &&
    isSemanticSearchResult(value.result) &&
    value.result.query === value.params.query
  )
}

/**
 * 只恢复同一公开用户、同一检索范围在当前标签页会话中的最后结果。
 * 缓存内容仍按不可信输入做完整运行时校验。
 */
export function readCachedSemanticSearch(
  ownerUserId: number,
  documentId?: number,
): RestoredSemanticSearch | null {
  if (!isInteger(ownerUserId, 1)) return null

  const key = cacheKey(ownerUserId)
  try {
    const raw = sessionStorage.getItem(key)
    if (!raw) return null
    const cached: unknown = JSON.parse(raw)
    if (!isCachedSemanticSearchEntry(cached)) {
      sessionStorage.removeItem(key)
      return null
    }
    if (cached.expiresAt <= Date.now()) {
      sessionStorage.removeItem(key)
      return null
    }
    if (cached.ownerUserId !== ownerUserId || cached.params.documentId !== (documentId ?? null)) {
      return null
    }

    return {
      params: {
        query: cached.params.query,
        documentId: cached.params.documentId ?? undefined,
        topK: cached.params.topK,
      },
      result: cached.result,
    }
  } catch {
    return null
  }
}

/** 用户关闭保留选项时立即删除自己的语义结果缓存。 */
export function clearCachedSemanticSearch(ownerUserId: number): void {
  if (!isInteger(ownerUserId, 1)) return
  try {
    sessionStorage.removeItem(cacheKey(ownerUserId))
  } catch {
    // 存储不可用时不阻塞页面状态。
  }
}

/** 保存当前用户当前标签页的最后一次成功语义检索；存储失败不影响主流程。 */
export function writeCachedSemanticSearch(
  ownerUserId: number,
  params: SemanticSearchParams,
  result: SemanticSearchResult,
): void {
  if (!isInteger(ownerUserId, 1)) return

  const entry: CachedSemanticSearchEntry = {
    version: cacheVersion,
    ownerUserId,
    retainedWithUserConsent: true,
    expiresAt: Date.now() + semanticCacheLifetimeMs,
    params: {
      query: params.query,
      documentId: params.documentId ?? null,
      topK: params.topK,
    },
    result,
  }

  try {
    sessionStorage.setItem(cacheKey(ownerUserId), JSON.stringify(entry))
  } catch {
    // 隐私模式、容量不足或存储不可用时仍保留内存中的本次结果。
  }
}
