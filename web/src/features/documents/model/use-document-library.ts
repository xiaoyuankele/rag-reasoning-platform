import { computed, onMounted, onScopeDispose, ref, shallowRef } from 'vue'
import type { DocumentPage } from '../../../entities/document/model/document'
import { toApiError } from '../../../shared/api/api-error'
import { listDocuments } from '../api/document-api'

export type DocumentListState = 'loading' | 'success' | 'empty' | 'error'

/** 管理当前用户的文档分页；导入状态由独立批量队列维护。 */
export function useDocumentLibrary(pageSize = 20) {
  const listState = ref<DocumentListState>('loading')
  const pageData = shallowRef<DocumentPage | null>(null)
  const listErrorMessage = ref('')
  let listController: AbortController | null = null

  const isListLoading = computed(() => listState.value === 'loading')

  async function loadPage(page = 1): Promise<void> {
    listController?.abort()
    const requestController = new AbortController()
    listController = requestController
    listState.value = 'loading'
    listErrorMessage.value = ''

    try {
      const result = await listDocuments({ page, pageSize }, requestController.signal)
      if (requestController.signal.aborted) return

      pageData.value = result
      listState.value = result.documents.length === 0 ? 'empty' : 'success'
    } catch (error) {
      if (requestController.signal.aborted) return

      pageData.value = null
      listErrorMessage.value = toApiError(error).message
      listState.value = 'error'
    } finally {
      if (listController === requestController) listController = null
    }
  }

  onMounted(() => void loadPage())
  onScopeDispose(() => listController?.abort())

  return {
    isListLoading,
    listErrorMessage,
    listState,
    loadPage,
    pageData,
  }
}
