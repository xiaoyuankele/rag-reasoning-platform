import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import type { SemanticSearchResult } from '../../../entities/search-result/model/search-result'
import { toApiError, type ApiError } from '../../../shared/api/api-error'
import {
  capacityFailureFromApiError,
  type CapacityFailure,
} from '../../../shared/api/capacity-error'
import { useRetryCooldown } from '../../../shared/api/use-retry-cooldown'
import { searchSemantically, type SemanticSearchParams } from '../api/search-semantically'
import { readCachedSemanticSearch, writeCachedSemanticSearch } from './semantic-search-cache'

export type SemanticSearchState = 'idle' | 'loading' | 'success' | 'empty' | 'error'

export interface SemanticSearchOptions {
  cacheOwnerUserId?: number
  initialDocumentId?: number
  restoreCachedResult?: boolean
  shouldRetainResult?: () => boolean
}

function semanticSearchErrorMessage(error: ApiError): string {
  if (error.kind === 'conflict') {
    return '所选范围的文档向量尚未就绪。请先完成向量化，再进行语义检索。'
  }
  if (error.kind === 'not-found') {
    return error.message === 'document not found'
      ? '所选文档不存在或当前账户不可访问，请重新选择范围。'
      : '当前后端没有开放语义检索接口。请确认 SEMANTIC_SEARCH_ENABLED 已经显式启用。'
  }
  if (error.kind === 'timeout') {
    return '语义检索超时。远程向量服务可能繁忙，请稍后重试。'
  }
  if (error.status === 502) {
    return '远程向量服务返回了无法使用的响应，请稍后重试。'
  }
  if (error.status === 503) {
    return '语义检索暂时不可用，可能是远程服务、额度或网络暂时异常。'
  }
  if (error.kind === 'invalid-response') return error.message
  if (error.kind === 'client') return '语义检索参数不符合后端要求，请检查问题和结果数量。'
  return error.message
}

/** 管理一次显式语义检索；不在页面挂载或输入变化时自动调用远程模型。 */
export function useSemanticSearch(options: SemanticSearchOptions = {}) {
  const state = ref<SemanticSearchState>('idle')
  const result = shallowRef<SemanticSearchResult | null>(null)
  const errorMessage = ref('')
  const requestId = ref<string | null>(null)
  const capacityFailure = ref<CapacityFailure | null>(null)
  const needsVectorization = ref(false)
  const retryCooldown = useRetryCooldown()
  let activeController: AbortController | null = null
  const restoredSearch =
    options.cacheOwnerUserId && options.restoreCachedResult !== false
      ? readCachedSemanticSearch(options.cacheOwnerUserId, options.initialDocumentId)
      : null
  let lastParams: SemanticSearchParams | null = restoredSearch?.params ?? null

  if (restoredSearch) {
    result.value = restoredSearch.result
    state.value = restoredSearch.result.hits.length === 0 ? 'empty' : 'success'
  }

  const isLoading = computed(() => state.value === 'loading')
  const retryAvailable = computed(() => state.value === 'error' && lastParams !== null)
  const canRetry = computed(
    () => retryAvailable.value && !retryCooldown.isCoolingDown.value && !isLoading.value,
  )

  async function search(params: SemanticSearchParams): Promise<void> {
    if (retryCooldown.isCoolingDown.value) return

    activeController?.abort()
    const requestController = new AbortController()
    activeController = requestController
    lastParams = { ...params }
    state.value = 'loading'
    result.value = null
    errorMessage.value = ''
    requestId.value = null
    capacityFailure.value = null
    needsVectorization.value = false
    retryCooldown.reset()

    try {
      const nextResult = await searchSemantically(params, requestController.signal)
      if (requestController.signal.aborted) return

      result.value = nextResult
      state.value = nextResult.hits.length === 0 ? 'empty' : 'success'
      if (options.cacheOwnerUserId && options.shouldRetainResult?.() === true) {
        writeCachedSemanticSearch(options.cacheOwnerUserId, params, nextResult)
      }
    } catch (error) {
      if (requestController.signal.aborted) return

      const apiError = toApiError(error)
      const capacity = capacityFailureFromApiError(apiError, 2)
      capacityFailure.value = capacity
      needsVectorization.value = apiError.kind === 'conflict'
      if (capacity) retryCooldown.start(capacity.retryAfterSeconds)
      errorMessage.value = capacity?.message ?? semanticSearchErrorMessage(apiError)
      requestId.value = apiError.requestId ?? null
      state.value = 'error'
    } finally {
      if (activeController === requestController) activeController = null
    }
  }

  async function retry(): Promise<void> {
    if (!lastParams || !canRetry.value) return
    await search(lastParams)
  }

  function retainCurrentResult(): void {
    if (
      !options.cacheOwnerUserId ||
      !lastParams ||
      !result.value ||
      (state.value !== 'success' && state.value !== 'empty')
    ) {
      return
    }
    writeCachedSemanticSearch(options.cacheOwnerUserId, lastParams, result.value)
  }

  function reset(options: { preserveCapacity?: boolean } = {}): void {
    activeController?.abort()
    activeController = null
    lastParams = null
    result.value = null
    if (options.preserveCapacity && capacityFailure.value) {
      state.value = 'error'
      return
    }

    capacityFailure.value = null
    needsVectorization.value = false
    retryCooldown.reset()
    errorMessage.value = ''
    requestId.value = null
    state.value = 'idle'
  }

  onScopeDispose(() => activeController?.abort())

  return {
    canRetry,
    capacityFailure,
    errorMessage,
    isCoolingDown: retryCooldown.isCoolingDown,
    isLoading,
    needsVectorization,
    requestId,
    reset,
    result,
    retainCurrentResult,
    retry,
    retryAfterSeconds: retryCooldown.remainingSeconds,
    retryAvailable,
    restoredParams: restoredSearch?.params ?? null,
    search,
    state,
  }
}
