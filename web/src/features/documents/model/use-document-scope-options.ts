import { computed, onMounted, onScopeDispose, ref, shallowRef } from 'vue'
import type { ResearchDocument } from '../../../entities/document/model/document'
import { toApiError } from '../../../shared/api/api-error'
import { listDocuments } from '../api/document-api'

export type DocumentScopeOptionsState = 'loading' | 'success' | 'empty' | 'error'

const maximumPageSize = 100

/**
 * 读取当前用户的全部文档分页，并只输出可用于关键词检索的 ready 文档。
 * 后端增加状态筛选接口前，由这一层集中处理分页和过滤，UI 不直接解释生命周期状态。
 */
export function useDocumentScopeOptions() {
  const state = ref<DocumentScopeOptionsState>('loading')
  const documents = shallowRef<ResearchDocument[]>([])
  const errorMessage = ref('')
  const requestId = ref<string | undefined>()
  let activeController: AbortController | null = null

  const isLoading = computed(() => state.value === 'loading')

  async function load(): Promise<void> {
    activeController?.abort()
    const requestController = new AbortController()
    activeController = requestController
    state.value = 'loading'
    errorMessage.value = ''
    requestId.value = undefined

    try {
      const readyDocuments = new Map<number, ResearchDocument>()
      let page = 1
      let totalPages = 1

      do {
        const result = await listDocuments(
          { page, pageSize: maximumPageSize },
          requestController.signal,
        )
        if (requestController.signal.aborted) return

        for (const document of result.documents) {
          if (document.status === 'ready') readyDocuments.set(document.id, document)
        }

        totalPages = Math.max(1, result.pagination.totalPages)
        page += 1
      } while (page <= totalPages)

      documents.value = [...readyDocuments.values()]
      state.value = documents.value.length > 0 ? 'success' : 'empty'
    } catch (error) {
      if (requestController.signal.aborted) return

      const apiError = toApiError(error)
      documents.value = []
      errorMessage.value = apiError.message
      requestId.value = apiError.requestId
      state.value = 'error'
    } finally {
      if (activeController === requestController) activeController = null
    }
  }

  onMounted(() => void load())
  onScopeDispose(() => activeController?.abort())

  return {
    documents,
    errorMessage,
    isLoading,
    load,
    requestId,
    state,
  }
}
