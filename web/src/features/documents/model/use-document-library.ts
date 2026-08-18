import { computed, onMounted, onScopeDispose, ref, shallowRef } from 'vue'
import type { DocumentPage, DocumentUploadResult } from '../../../entities/document/model/document'
import { toApiError } from '../../../shared/api/api-error'
import { listDocuments, uploadDocument } from '../api/document-api'

export type DocumentListState = 'loading' | 'success' | 'empty' | 'error'

export interface UploadNotice extends DocumentUploadResult {
  selectedFileName: string
}

function presentUploadError(error: unknown): { message: string; requestId?: string } {
  const apiError = toApiError(error)

  if (apiError.status === 413) {
    return { message: '文件超过服务端允许的上传大小。', requestId: apiError.requestId }
  }
  if (apiError.status === 415) {
    return {
      message: '文件内容不受支持，请选择有效的 PDF、Markdown 或纯文本文件。',
      requestId: apiError.requestId,
    }
  }
  if (apiError.status === 400) {
    return { message: '请选择内容有效的文件后重试。', requestId: apiError.requestId }
  }

  return { message: apiError.message, requestId: apiError.requestId }
}

/** 管理文档分页和单文件上传；重复内容作为成功结果保留，不进入错误状态。 */
export function useDocumentLibrary(pageSize = 20) {
  const listState = ref<DocumentListState>('loading')
  const pageData = shallowRef<DocumentPage | null>(null)
  const listErrorMessage = ref('')
  const uploadErrorMessage = ref('')
  const uploadRequestId = ref<string | undefined>()
  const uploadNotice = shallowRef<UploadNotice | null>(null)
  const isUploading = ref(false)
  let listController: AbortController | null = null
  let uploadController: AbortController | null = null

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

  async function uploadFile(file: File): Promise<boolean> {
    uploadController?.abort()
    const requestController = new AbortController()
    uploadController = requestController
    isUploading.value = true
    uploadErrorMessage.value = ''
    uploadRequestId.value = undefined
    uploadNotice.value = null

    try {
      const result = await uploadDocument(file, requestController.signal)
      if (requestController.signal.aborted) return false

      uploadNotice.value = { ...result, selectedFileName: file.name }
      await loadPage(1)
      return true
    } catch (error) {
      if (requestController.signal.aborted) return false

      const presentation = presentUploadError(error)
      uploadErrorMessage.value = presentation.message
      uploadRequestId.value = presentation.requestId
      return false
    } finally {
      if (uploadController === requestController) {
        uploadController = null
        isUploading.value = false
      }
    }
  }

  onMounted(() => void loadPage())
  onScopeDispose(() => {
    listController?.abort()
    uploadController?.abort()
  })

  return {
    isListLoading,
    isUploading,
    listErrorMessage,
    listState,
    loadPage,
    pageData,
    uploadErrorMessage,
    uploadFile,
    uploadNotice,
    uploadRequestId,
  }
}
