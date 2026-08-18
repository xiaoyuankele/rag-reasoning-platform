import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import type { DocumentChunkPage } from '../../../entities/document/model/document-chunk'
import type { ResearchDocument } from '../../../entities/document/model/document'
import { toApiError } from '../../../shared/api/api-error'
import { listDocumentChunks } from '../api/document-chunk-api'
import { deleteDocument, getDocument } from '../api/document-api'
import { useProcessingJob } from './use-processing-job'

export type DetailLoadState = 'idle' | 'loading' | 'success' | 'error'
export type ChunkLoadState = 'idle' | 'loading' | 'success' | 'empty' | 'error'

interface UseDocumentDetailOptions {
  chunkPageSize?: number
  pollIntervalMs?: number
}

/** 编排详情、解析任务、文本块和删除；组件只消费这里输出的稳定状态。 */
export function useDocumentDetail(
  documentId: Ref<number | null>,
  options: UseDocumentDetailOptions = {},
) {
  const chunkPageSize = options.chunkPageSize ?? 10
  const pollIntervalMs = options.pollIntervalMs ?? 1_500
  const detailState = ref<DetailLoadState>('idle')
  const document = shallowRef<ResearchDocument | null>(null)
  const detailErrorMessage = ref('')
  const detailRequestId = ref<string | undefined>()
  const chunkState = ref<ChunkLoadState>('idle')
  const chunkPage = shallowRef<DocumentChunkPage | null>(null)
  const chunkErrorMessage = ref('')
  const chunkRequestId = ref<string | undefined>()
  const isDeleting = ref(false)
  const deleteErrorMessage = ref('')
  const deleteRequestId = ref<string | undefined>()
  const isRecoveringUnknownJob = ref(false)
  let recoveryBaselineUpdatedAt = 0
  let recoveryObservedProcessing = false
  let detailController: AbortController | null = null
  let chunkController: AbortController | null = null
  let deleteController: AbortController | null = null
  let documentPollTimer: ReturnType<typeof setTimeout> | undefined

  const processing = useProcessingJob({
    pollIntervalMs,
    onJobStatusChange: async () => {
      const refreshedDocument = await fetchDocument(false)
      if (refreshedDocument) await synchronizeChunks(refreshedDocument)
    },
    onQueueConflict: async () => {
      recoveryBaselineUpdatedAt = document.value?.updatedAt.getTime() ?? Date.now()
      recoveryObservedProcessing = document.value?.status === 'processing'
      isRecoveringUnknownJob.value = true
      scheduleDocumentPoll()
    },
  })

  const canStartProcessing = computed(
    () =>
      (document.value?.status === 'uploaded' || document.value?.status === 'failed') &&
      processing.state.value !== 'queueing' &&
      !processing.hasActiveJob.value &&
      !isRecoveringUnknownJob.value,
  )

  const canDelete = computed(
    () =>
      document.value !== null &&
      document.value.status !== 'processing' &&
      processing.state.value !== 'queueing' &&
      !processing.hasActiveJob.value &&
      !isRecoveringUnknownJob.value &&
      !isDeleting.value,
  )

  function stopDocumentPolling(): void {
    if (documentPollTimer !== undefined) {
      clearTimeout(documentPollTimer)
      documentPollTimer = undefined
    }
  }

  function scheduleDocumentPoll(): void {
    stopDocumentPolling()
    if (documentId.value === null) return
    documentPollTimer = setTimeout(() => void pollDocument(), pollIntervalMs)
  }

  function shouldContinueRecovery(currentDocument: ResearchDocument): boolean {
    if (currentDocument.status === 'processing') {
      recoveryObservedProcessing = true
      return true
    }
    if (currentDocument.status === 'ready') return false
    if (currentDocument.status === 'failed') {
      return (
        !recoveryObservedProcessing &&
        currentDocument.updatedAt.getTime() <= recoveryBaselineUpdatedAt
      )
    }
    return true
  }

  async function pollDocument(): Promise<void> {
    const refreshedDocument = await fetchDocument(false)
    if (!refreshedDocument) return
    await synchronizeChunks(refreshedDocument)

    const shouldContinue = isRecoveringUnknownJob.value
      ? shouldContinueRecovery(refreshedDocument)
      : refreshedDocument.status === 'processing'

    if (shouldContinue) {
      scheduleDocumentPoll()
    } else {
      isRecoveringUnknownJob.value = false
      stopDocumentPolling()
    }
  }

  async function fetchDocument(showLoading: boolean): Promise<ResearchDocument | null> {
    const activeDocumentId = documentId.value
    if (activeDocumentId === null) return null

    detailController?.abort()
    const controller = new AbortController()
    detailController = controller
    if (showLoading) detailState.value = 'loading'
    detailErrorMessage.value = ''
    detailRequestId.value = undefined

    try {
      const result = await getDocument(activeDocumentId, controller.signal)
      if (controller.signal.aborted || documentId.value !== activeDocumentId) return null
      document.value = result
      detailState.value = 'success'
      return result
    } catch (error) {
      if (controller.signal.aborted || documentId.value !== activeDocumentId) return null
      const apiError = toApiError(error)
      document.value = null
      detailState.value = 'error'
      detailErrorMessage.value =
        apiError.status === 404 ? '文档不存在、已被删除，或不属于当前账户。' : apiError.message
      detailRequestId.value = apiError.requestId
      stopDocumentPolling()
      return null
    } finally {
      if (detailController === controller) detailController = null
    }
  }

  function resetChunks(): void {
    chunkController?.abort()
    chunkController = null
    chunkState.value = 'idle'
    chunkPage.value = null
    chunkErrorMessage.value = ''
    chunkRequestId.value = undefined
  }

  async function synchronizeChunks(currentDocument: ResearchDocument): Promise<void> {
    if (currentDocument.status === 'ready') {
      if (chunkState.value === 'idle') await loadChunks(1)
      return
    }
    resetChunks()
  }

  async function loadChunks(page = 1): Promise<void> {
    const activeDocument = document.value
    if (!activeDocument || activeDocument.status !== 'ready') {
      resetChunks()
      return
    }

    chunkController?.abort()
    const controller = new AbortController()
    chunkController = controller
    chunkState.value = 'loading'
    chunkErrorMessage.value = ''
    chunkRequestId.value = undefined

    try {
      const result = await listDocumentChunks(
        { documentId: activeDocument.id, page, pageSize: chunkPageSize },
        controller.signal,
      )
      if (controller.signal.aborted || document.value?.id !== activeDocument.id) return
      chunkPage.value = result
      chunkState.value = result.chunks.length === 0 ? 'empty' : 'success'
    } catch (error) {
      if (controller.signal.aborted || document.value?.id !== activeDocument.id) return
      const apiError = toApiError(error)
      chunkPage.value = null
      chunkState.value = 'error'
      chunkErrorMessage.value =
        apiError.status === 409 ? '文本块尚未准备好，请刷新文档状态后重试。' : apiError.message
      chunkRequestId.value = apiError.requestId
    } finally {
      if (chunkController === controller) chunkController = null
    }
  }

  async function load(): Promise<void> {
    stopDocumentPolling()
    const result = await fetchDocument(true)
    if (!result) return
    await synchronizeChunks(result)
    if (result.status === 'processing') scheduleDocumentPoll()
  }

  async function startProcessing(): Promise<boolean> {
    const activeDocumentId = document.value?.id
    if (!activeDocumentId || !canStartProcessing.value) return false
    stopDocumentPolling()
    resetChunks()
    return processing.queue(activeDocumentId)
  }

  async function remove(): Promise<boolean> {
    const activeDocumentId = document.value?.id
    if (!activeDocumentId || !canDelete.value) return false

    deleteController?.abort()
    const controller = new AbortController()
    deleteController = controller
    isDeleting.value = true
    deleteErrorMessage.value = ''
    deleteRequestId.value = undefined

    try {
      await deleteDocument(activeDocumentId, controller.signal)
      if (controller.signal.aborted || document.value?.id !== activeDocumentId) return false
      stopDocumentPolling()
      processing.reset()
      resetChunks()
      document.value = null
      detailState.value = 'idle'
      return true
    } catch (error) {
      if (controller.signal.aborted) return false
      const apiError = toApiError(error)
      deleteErrorMessage.value =
        apiError.status === 404 ? '文档已经不存在，刷新列表后即可同步。' : apiError.message
      deleteRequestId.value = apiError.requestId
      return false
    } finally {
      if (deleteController === controller) {
        deleteController = null
        isDeleting.value = false
      }
    }
  }

  function reset(): void {
    stopDocumentPolling()
    detailController?.abort()
    detailController = null
    deleteController?.abort()
    deleteController = null
    processing.reset()
    resetChunks()
    detailState.value = 'idle'
    document.value = null
    detailErrorMessage.value = ''
    detailRequestId.value = undefined
    isDeleting.value = false
    deleteErrorMessage.value = ''
    deleteRequestId.value = undefined
    isRecoveringUnknownJob.value = false
    recoveryBaselineUpdatedAt = 0
    recoveryObservedProcessing = false
  }

  watch(
    documentId,
    (nextDocumentId) => {
      reset()
      if (nextDocumentId !== null) void load()
    },
    { immediate: true },
  )

  onScopeDispose(reset)

  return {
    canDelete,
    canStartProcessing,
    chunkErrorMessage,
    chunkPage,
    chunkRequestId,
    chunkState,
    deleteErrorMessage,
    deleteRequestId,
    detailErrorMessage,
    detailRequestId,
    detailState,
    document,
    isDeleting,
    isRecoveringUnknownJob,
    load,
    loadChunks,
    processing,
    remove,
    startProcessing,
  }
}
