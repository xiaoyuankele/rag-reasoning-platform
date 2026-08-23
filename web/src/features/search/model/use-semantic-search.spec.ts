import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SemanticSearchResult } from '../../../entities/search-result/model/search-result'
import { ApiError } from '../../../shared/api/api-error'
import { searchSemantically } from '../api/search-semantically'
import { useSemanticSearch } from './use-semantic-search'

vi.mock('../api/search-semantically', () => ({ searchSemantically: vi.fn() }))

const searchSemanticallyMock = vi.mocked(searchSemantically)
const result: SemanticSearchResult = {
  query: '问题',
  hits: [
    {
      chunkId: 11,
      documentId: 7,
      chunkIndex: 2,
      title: null,
      originalName: 'maglev.pdf',
      mimeType: 'application/pdf',
      content: '相关文本',
      pageStart: 3,
      pageEnd: 3,
      similarity: 0.9,
    },
  ],
}

beforeEach(() => {
  sessionStorage.clear()
  searchSemanticallyMock.mockReset()
})
afterEach(() => vi.useRealTimers())

describe('useSemanticSearch', () => {
  it('区分有命中和后端正常零命中', async () => {
    const scope = effectScope()
    const model = scope.run(() => useSemanticSearch())!
    searchSemanticallyMock.mockResolvedValueOnce(result)
    await model.search({ query: '问题', topK: 5 })
    expect(model.state.value).toBe('success')
    expect(model.result.value).toEqual(result)

    searchSemanticallyMock.mockResolvedValueOnce({ ...result, hits: [] })
    await model.search({ query: '另一个问题', topK: 3 })
    expect(model.state.value).toBe('empty')
    scope.stop()
  })

  it('把 409 转为向量化引导并保留请求编号', async () => {
    const scope = effectScope()
    const model = scope.run(() => useSemanticSearch())!
    searchSemanticallyMock.mockRejectedValueOnce(
      new ApiError('conflict', 'document embeddings are not ready', {
        status: 409,
        requestId: 'semantic-409',
      }),
    )

    await model.search({ query: '问题', documentId: 7, topK: 5 })

    expect(model.needsVectorization.value).toBe(true)
    expect(model.errorMessage.value).toContain('完成向量化')
    expect(model.requestId.value).toBe('semantic-409')
    scope.stop()
  })

  it('容量满载时遵守 Retry-After，不自动或提前重放', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const model = scope.run(() => useSemanticSearch())!
    searchSemanticallyMock
      .mockRejectedValueOnce(
        new ApiError('server', 'busy', {
          status: 503,
          code: 'embedding_provider_capacity_exhausted',
          requestId: 'semantic-capacity-1',
          retryAfterSeconds: 2,
        }),
      )
      .mockResolvedValueOnce(result)

    await model.search({ query: '问题', topK: 5 })
    expect(model.capacityFailure.value?.title).toContain('在线向量服务')
    expect(model.canRetry.value).toBe(false)
    expect(model.retryAfterSeconds.value).toBe(2)

    await model.retry()
    expect(searchSemanticallyMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(2_000)
    expect(model.canRetry.value).toBe(true)
    expect(searchSemanticallyMock).toHaveBeenCalledTimes(1)

    await model.retry()
    expect(searchSemanticallyMock).toHaveBeenCalledTimes(2)
    expect(model.state.value).toBe('success')
    scope.stop()
  })

  it('只为同一用户和同一文档范围恢复经过校验的会话缓存', async () => {
    searchSemanticallyMock.mockResolvedValueOnce(result)
    const firstScope = effectScope()
    const firstModel = firstScope.run(() =>
      useSemanticSearch({ cacheOwnerUserId: 17, initialDocumentId: 7 }),
    )!
    await firstModel.search({ query: '问题', documentId: 7, topK: 5 })
    firstScope.stop()

    const restoredScope = effectScope()
    const restoredModel = restoredScope.run(() =>
      useSemanticSearch({ cacheOwnerUserId: 17, initialDocumentId: 7 }),
    )!
    expect(restoredModel.state.value).toBe('success')
    expect(restoredModel.result.value).toEqual(result)
    expect(restoredModel.restoredParams).toEqual({ query: '问题', documentId: 7, topK: 5 })
    restoredScope.stop()

    const otherUserScope = effectScope()
    const otherUserModel = otherUserScope.run(() =>
      useSemanticSearch({ cacheOwnerUserId: 18, initialDocumentId: 7 }),
    )!
    expect(otherUserModel.state.value).toBe('idle')
    otherUserScope.stop()

    const otherDocumentScope = effectScope()
    const otherDocumentModel = otherDocumentScope.run(() =>
      useSemanticSearch({ cacheOwnerUserId: 17, initialDocumentId: 8 }),
    )!
    expect(otherDocumentModel.state.value).toBe('idle')
    otherDocumentScope.stop()
  })
})
