import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import type { KeywordSearchPage } from '../../../entities/search-result/model/search-result'
import { toApiError } from '../../../shared/api/api-error'
import { searchKeywords, type KeywordSearchParams } from '../api/search-keywords'

export type KeywordSearchState = 'idle' | 'loading' | 'success' | 'empty' | 'error'

/** 管理单次关键词检索状态，并取消被新查询替代的旧请求。 */
export function useKeywordSearch() {
  const state = ref<KeywordSearchState>('idle')
  const resultPage = shallowRef<KeywordSearchPage | null>(null)
  const errorMessage = ref('')
  let activeController: AbortController | null = null

  const isLoading = computed(() => state.value === 'loading')

  async function search(params: KeywordSearchParams): Promise<void> {
    activeController?.abort()
    const requestController = new AbortController()
    activeController = requestController

    state.value = 'loading'
    errorMessage.value = ''

    try {
      const nextPage = await searchKeywords(params, requestController.signal)
      if (requestController.signal.aborted) return

      resultPage.value = nextPage
      state.value = nextPage.results.length === 0 ? 'empty' : 'success'
    } catch (error) {
      if (requestController.signal.aborted) return

      resultPage.value = null
      errorMessage.value = toApiError(error).message
      state.value = 'error'
    } finally {
      if (activeController === requestController) {
        activeController = null
      }
    }
  }

  function reset(): void {
    activeController?.abort()
    activeController = null
    resultPage.value = null
    errorMessage.value = ''
    state.value = 'idle'
  }

  onScopeDispose(() => {
    activeController?.abort()
  })

  return {
    errorMessage,
    isLoading,
    reset,
    resultPage,
    search,
    state,
  }
}
