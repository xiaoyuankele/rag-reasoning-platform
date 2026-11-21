import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import type { ResearchDocument } from '../../../entities/document/model/document'
import type { ProcessingJob } from '../../../entities/processing-job/model/processing-job'
import { ApiError, toApiError } from '../../../shared/api/api-error'
import { getDocument, uploadDocument } from '../api/document-api'
import { preflightDocument } from '../api/document-preflight-api'
import { getProcessingJob, queueDocumentProcessing } from '../api/processing-api'
import {
  createFileHashWorkerClient,
  type FileHashClient,
  type FileHashProgress,
} from './file-hash-worker-client'

export type DocumentImportState =
  | 'waiting'
  | 'hashing'
  | 'checking'
  | 'duplicate'
  | 'uploading'
  | 'queueing'
  | 'queued'
  | 'processing'
  | 'ready'
  | 'hash-failed'
  | 'check-failed'
  | 'upload-failed'
  | 'queue-failed'
  | 'process-failed'
  | 'stopped'

export interface DocumentImportItem {
  localId: string
  file: File
  state: DocumentImportState
  document: ResearchDocument | null
  job: ProcessingJob | null
  duplicate: boolean
  sha256: string | null
  hashProgress: FileHashProgress | null
  errorMessage: string
  requestId?: string
  warningMessage: string
  warningRequestId?: string
}

export interface DocumentImportSummary {
  total: number
  waiting: number
  active: number
  ready: number
  failed: number
  duplicate: number
  stopped: number
}

interface TrackedImport {
  documentId: number
  jobId?: number
  baselineUpdatedAt: number
  observedProcessing: boolean
}

interface UseDocumentImportQueueOptions {
  maxFiles?: number
  maxFileSizeBytes?: number
  uploadConcurrency?: number
  pollIntervalMs?: number
  pollBatchSize?: number
  fileHashClient?: FileHashClient
}

const supportedExtensions = new Set(['pdf', 'md', 'markdown', 'txt'])
const failedStates = new Set<DocumentImportState>([
  'hash-failed',
  'check-failed',
  'upload-failed',
  'queue-failed',
  'process-failed',
])
const activeStates = new Set<DocumentImportState>([
  'hashing',
  'checking',
  'uploading',
  'queueing',
  'queued',
  'processing',
])

let importItemSequence = 0

function createLocalId(): string {
  importItemSequence += 1
  return `document-import-${Date.now()}-${importItemSequence}`
}

function validateFile(file: File, maxFileSizeBytes: number): string | null {
  const extension = file.name.split('.').pop()?.toLowerCase() ?? ''
  if (!supportedExtensions.has(extension)) {
    return '仅支持 PDF、Markdown 和纯文本文件。'
  }
  if (file.size <= 0) return '文件内容为空，无法导入。'
  if (file.size > maxFileSizeBytes) return '文件超过当前 200 MiB 导入上限。'
  return null
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
    return { message: '文件内容无效，无法保存。', requestId: apiError.requestId }
  }
  return { message: apiError.message, requestId: apiError.requestId }
}

function canFailOpenPreflight(error: unknown): boolean {
  const apiError = toApiError(error)
  return (
    apiError.kind === 'network' ||
    apiError.kind === 'timeout' ||
    (apiError.status !== undefined && apiError.status >= 500)
  )
}

function presentPreflightError(error: unknown): { message: string; requestId?: string } {
  const apiError = toApiError(error)
  if (apiError.status === 413 || apiError.code === 'file_too_large') {
    return { message: '文件超过服务端允许的上传大小。', requestId: apiError.requestId }
  }
  if (apiError.status === 400 || apiError.code === 'invalid_document_preflight') {
    return {
      message: '文件摘要或大小未被后端接受，已停止上传。',
      requestId: apiError.requestId,
    }
  }
  return { message: apiError.message, requestId: apiError.requestId }
}

/**
 * 用现有单文件接口编排批量导入。
 * 上传使用有限并发；任务状态由一个集中轮询器分批刷新，避免每个队列项各建计时器。
 */
export function useDocumentImportQueue(options: UseDocumentImportQueueOptions = {}) {
  const maxFiles = options.maxFiles ?? 20
  const maxFileSizeBytes = options.maxFileSizeBytes ?? 200 * 1024 * 1024
  const uploadConcurrency = Math.max(1, options.uploadConcurrency ?? 2)
  const pollIntervalMs = options.pollIntervalMs ?? 2_000
  const pollBatchSize = Math.max(1, options.pollBatchSize ?? 4)
  const fileHashClient = options.fileHashClient ?? createFileHashWorkerClient()
  const items = shallowRef<DocumentImportItem[]>([])
  const selectionMessage = ref('')
  const isDispatching = ref(false)
  const trackedImports = new Map<string, TrackedImport>()
  const activeRequestControllers = new Map<string, AbortController>()
  const trackingControllers = new Map<string, AbortController>()
  let trackingTimer: ReturnType<typeof setTimeout> | undefined
  let pollCursor = 0
  let stopRequested = false
  let disposed = false

  const summary = computed<DocumentImportSummary>(() => ({
    total: items.value.length,
    waiting: items.value.filter((item) => item.state === 'waiting').length,
    active: items.value.filter((item) => activeStates.has(item.state)).length,
    ready: items.value.filter((item) => item.state === 'ready' || item.state === 'duplicate')
      .length,
    failed: items.value.filter((item) => failedStates.has(item.state)).length,
    duplicate: items.value.filter((item) => item.duplicate).length,
    stopped: items.value.filter((item) => item.state === 'stopped').length,
  }))

  const canStart = computed(
    () => !isDispatching.value && items.value.some((item) => item.state === 'waiting'),
  )
  const hasRetryableItems = computed(() => items.value.some((item) => failedStates.has(item.state)))
  const hasStoppedItems = computed(() => items.value.some((item) => item.state === 'stopped'))
  const canStop = computed(
    () =>
      isDispatching.value ||
      items.value.some((item) =>
        ['waiting', 'hashing', 'checking', 'uploading'].includes(item.state),
      ),
  )

  function findItem(localId: string): DocumentImportItem | undefined {
    return items.value.find((item) => item.localId === localId)
  }

  function updateItem(localId: string, changes: Partial<DocumentImportItem>): void {
    if (disposed) return
    items.value = items.value.map((item) =>
      item.localId === localId ? { ...item, ...changes } : item,
    )
  }

  function addFiles(files: File[]): void {
    selectionMessage.value = ''
    if (files.length === 0) return

    const availableSlots = Math.max(0, maxFiles - items.value.length)
    const acceptedFiles = files.slice(0, availableSlots)
    const rejectedMessages: string[] = []
    const newItems: DocumentImportItem[] = []

    if (files.length > availableSlots) {
      rejectedMessages.push(`单批最多保留 ${maxFiles} 份文件，超出的文件未加入队列。`)
    }

    for (const file of acceptedFiles) {
      const validationMessage = validateFile(file, maxFileSizeBytes)
      if (validationMessage) {
        rejectedMessages.push(`“${file.name}”：${validationMessage}`)
        continue
      }

      newItems.push({
        localId: createLocalId(),
        file,
        state: 'waiting',
        document: null,
        job: null,
        duplicate: false,
        sha256: null,
        hashProgress: null,
        errorMessage: '',
        warningMessage: '',
      })
    }

    items.value = [...items.value, ...newItems]
    selectionMessage.value = rejectedMessages.join(' ')
  }

  function removeItem(localId: string): void {
    const item = findItem(localId)
    if (!item || activeStates.has(item.state)) return
    trackedImports.delete(localId)
    items.value = items.value.filter((candidate) => candidate.localId !== localId)
  }

  function clearFinished(): void {
    const removableStates = new Set<DocumentImportState>([
      'ready',
      'duplicate',
      'hash-failed',
      'check-failed',
      'upload-failed',
      'queue-failed',
      'process-failed',
      'stopped',
    ])
    items.value = items.value.filter((item) => !removableStates.has(item.state))
  }

  function ensureTrackingPoll(): void {
    if (disposed || trackingTimer !== undefined || trackedImports.size === 0) return
    trackingTimer = setTimeout(() => void pollTrackedImports(), pollIntervalMs)
  }

  function trackImport(localId: string, trackedImport: TrackedImport): void {
    trackedImports.set(localId, trackedImport)
    ensureTrackingPoll()
  }

  async function refreshTerminalDocument(
    localId: string,
    trackedImport: TrackedImport,
    signal: AbortSignal,
  ): Promise<boolean> {
    const refreshedDocument = await getDocument(trackedImport.documentId, signal)
    if (signal.aborted) return false

    if (refreshedDocument.status === 'ready') {
      updateItem(localId, {
        document: refreshedDocument,
        state: 'ready',
        errorMessage: '',
        requestId: undefined,
      })
      trackedImports.delete(localId)
      return true
    }

    if (refreshedDocument.status === 'failed') {
      updateItem(localId, {
        document: refreshedDocument,
        state: 'process-failed',
        errorMessage: refreshedDocument.errorMessage ?? '文档解析失败，可以重试。',
        requestId: undefined,
      })
      trackedImports.delete(localId)
      return true
    }

    updateItem(localId, {
      document: refreshedDocument,
      state: refreshedDocument.status === 'processing' ? 'processing' : 'queued',
      errorMessage: '',
      requestId: undefined,
    })
    return false
  }

  async function pollKnownJob(
    localId: string,
    trackedImport: TrackedImport,
    controller: AbortController,
  ): Promise<void> {
    const job = await getProcessingJob(trackedImport.jobId!, controller.signal)
    if (controller.signal.aborted) return

    if (job.status === 'failed') {
      updateItem(localId, {
        job,
        state: 'process-failed',
        errorMessage: job.errorMessage ?? '文档解析失败，可以重试。',
        requestId: undefined,
      })
      trackedImports.delete(localId)
      return
    }

    if (job.status === 'succeeded') {
      updateItem(localId, { job, errorMessage: '', requestId: undefined })
      await refreshTerminalDocument(localId, trackedImport, controller.signal)
      return
    }

    updateItem(localId, {
      job,
      state: job.status,
      errorMessage: '',
      requestId: undefined,
    })
  }

  async function pollDocumentStatus(
    localId: string,
    trackedImport: TrackedImport,
    controller: AbortController,
  ): Promise<void> {
    const refreshedDocument = await getDocument(trackedImport.documentId, controller.signal)
    if (controller.signal.aborted) return

    if (refreshedDocument.status === 'ready') {
      updateItem(localId, {
        document: refreshedDocument,
        state: 'ready',
        errorMessage: '',
        requestId: undefined,
      })
      trackedImports.delete(localId)
      return
    }

    if (refreshedDocument.status === 'processing') {
      trackedImport.observedProcessing = true
      updateItem(localId, {
        document: refreshedDocument,
        state: 'processing',
        errorMessage: '',
        requestId: undefined,
      })
      return
    }

    if (
      refreshedDocument.status === 'failed' &&
      (trackedImport.observedProcessing ||
        refreshedDocument.updatedAt.getTime() > trackedImport.baselineUpdatedAt)
    ) {
      updateItem(localId, {
        document: refreshedDocument,
        state: 'process-failed',
        errorMessage: refreshedDocument.errorMessage ?? '文档解析失败，可以重试。',
        requestId: undefined,
      })
      trackedImports.delete(localId)
      return
    }

    updateItem(localId, {
      document: refreshedDocument,
      state: 'queued',
      errorMessage: '',
      requestId: undefined,
    })
  }

  async function pollTrackedItem(localId: string, trackedImport: TrackedImport): Promise<void> {
    if (!findItem(localId)) {
      trackedImports.delete(localId)
      return
    }

    const controller = new AbortController()
    trackingControllers.set(localId, controller)
    try {
      if (trackedImport.jobId) {
        await pollKnownJob(localId, trackedImport, controller)
      } else {
        await pollDocumentStatus(localId, trackedImport, controller)
      }
    } catch (error) {
      if (controller.signal.aborted) return
      const apiError = toApiError(error)
      updateItem(localId, {
        errorMessage: `状态刷新暂时失败：${apiError.message}`,
        requestId: apiError.requestId,
      })
    } finally {
      if (trackingControllers.get(localId) === controller) trackingControllers.delete(localId)
    }
  }

  async function pollTrackedImports(): Promise<void> {
    trackingTimer = undefined
    const entries = [...trackedImports.entries()]
    if (entries.length === 0 || disposed) return

    const count = Math.min(pollBatchSize, entries.length)
    const start = pollCursor % entries.length
    const selectedEntries = Array.from(
      { length: count },
      (_, index) => entries[(start + index) % entries.length]!,
    )
    pollCursor = (start + count) % entries.length
    await Promise.all(
      selectedEntries.map(([localId, trackedImport]) => pollTrackedItem(localId, trackedImport)),
    )
    ensureTrackingPoll()
  }

  async function queueItem(localId: string, document: ResearchDocument): Promise<void> {
    const controller = new AbortController()
    activeRequestControllers.set(localId, controller)
    updateItem(localId, {
      document,
      state: 'queueing',
      errorMessage: '',
      requestId: undefined,
    })

    try {
      const job = await queueDocumentProcessing(document.id, controller.signal)
      if (controller.signal.aborted) {
        updateItem(localId, { state: 'stopped', errorMessage: '操作已停止。' })
        return
      }

      updateItem(localId, {
        job,
        state: job.status === 'processing' ? 'processing' : 'queued',
        errorMessage: '',
        requestId: undefined,
      })
      if (job.status === 'failed') {
        updateItem(localId, {
          state: 'process-failed',
          errorMessage: job.errorMessage ?? '文档解析失败，可以重试。',
        })
      } else if (job.status === 'succeeded') {
        trackImport(localId, {
          documentId: document.id,
          jobId: job.id,
          baselineUpdatedAt: document.updatedAt.getTime(),
          observedProcessing: true,
        })
      } else {
        trackImport(localId, {
          documentId: document.id,
          jobId: job.id,
          baselineUpdatedAt: document.updatedAt.getTime(),
          observedProcessing: job.status === 'processing',
        })
      }
    } catch (error) {
      if (controller.signal.aborted) {
        updateItem(localId, { state: 'stopped', errorMessage: '操作已停止。' })
        return
      }

      const apiError = toApiError(error)
      if (apiError.status === 409) {
        updateItem(localId, {
          state: 'queued',
          errorMessage: '已有解析任务，正在等待文档状态更新。',
          requestId: apiError.requestId,
        })
        trackImport(localId, {
          documentId: document.id,
          baselineUpdatedAt: document.updatedAt.getTime(),
          observedProcessing: document.status === 'processing',
        })
      } else {
        updateItem(localId, {
          state: 'queue-failed',
          errorMessage: apiError.message,
          requestId: apiError.requestId,
        })
      }
    } finally {
      if (activeRequestControllers.get(localId) === controller) {
        activeRequestControllers.delete(localId)
      }
    }
  }

  async function continueWithDocument(
    localId: string,
    document: ResearchDocument,
    duplicate: boolean,
    preflightHit = false,
  ): Promise<void> {
    updateItem(localId, {
      document,
      duplicate,
      errorMessage: '',
      requestId: undefined,
    })

    if (document.status === 'ready') {
      updateItem(localId, { state: preflightHit ? 'duplicate' : 'ready' })
      return
    }

    if (document.status === 'processing') {
      updateItem(localId, { state: 'processing' })
      trackImport(localId, {
        documentId: document.id,
        baselineUpdatedAt: document.updatedAt.getTime(),
        observedProcessing: true,
      })
      return
    }

    await queueItem(localId, document)
  }

  async function uploadAndContinue(
    localId: string,
    file: File,
    sha256: string,
    controller: AbortController,
  ): Promise<void> {
    updateItem(localId, {
      state: 'uploading',
      hashProgress: null,
      errorMessage: '',
      requestId: undefined,
    })

    try {
      const result = await uploadDocument(file, controller.signal)
      if (controller.signal.aborted) {
        updateItem(localId, { state: 'stopped', errorMessage: '上传已停止。' })
        return
      }

      if (result.document.sha256 !== sha256 || result.document.sizeBytes !== file.size) {
        throw new ApiError('invalid-response', '后端上传结果与本地文件摘要不一致。')
      }

      await continueWithDocument(localId, result.document, result.duplicate)
    } catch (error) {
      if (controller.signal.aborted) {
        updateItem(localId, { state: 'stopped', errorMessage: '上传已停止。' })
        return
      }
      const presentation = presentUploadError(error)
      updateItem(localId, {
        state: 'upload-failed',
        errorMessage: presentation.message,
        requestId: presentation.requestId,
      })
    }
  }

  async function processItem(localId: string): Promise<void> {
    const currentItem = findItem(localId)
    if (!currentItem) return

    if (currentItem.document) {
      await queueItem(localId, currentItem.document)
      return
    }

    const controller = new AbortController()
    activeRequestControllers.set(localId, controller)
    updateItem(localId, {
      state: 'hashing',
      hashProgress: { processedBytes: 0, totalBytes: currentItem.file.size },
      errorMessage: '',
      requestId: undefined,
      warningMessage: '',
      warningRequestId: undefined,
    })

    try {
      let sha256: string
      try {
        sha256 = await fileHashClient.hash(currentItem.file, {
          jobId: localId,
          signal: controller.signal,
          onProgress: (progress) => {
            if (findItem(localId)?.state === 'hashing') {
              updateItem(localId, { hashProgress: progress })
            }
          },
        })
      } catch (error) {
        if (controller.signal.aborted) {
          updateItem(localId, { state: 'stopped', errorMessage: '本地检查已停止。' })
          return
        }
        updateItem(localId, {
          state: 'hash-failed',
          errorMessage:
            error instanceof Error && error.message.trim()
              ? error.message
              : '无法计算文件摘要，请重试。',
        })
        return
      }

      if (controller.signal.aborted) {
        updateItem(localId, { state: 'stopped', errorMessage: '本地检查已停止。' })
        return
      }

      updateItem(localId, {
        state: 'checking',
        sha256,
        hashProgress: { processedBytes: currentItem.file.size, totalBytes: currentItem.file.size },
      })

      try {
        const preflight = await preflightDocument(
          { sha256, sizeBytes: currentItem.file.size },
          controller.signal,
        )
        if (controller.signal.aborted) {
          updateItem(localId, { state: 'stopped', errorMessage: '预检已停止。' })
          return
        }

        if (preflight.exists && preflight.document) {
          await continueWithDocument(localId, preflight.document, true, true)
          return
        }
      } catch (error) {
        if (controller.signal.aborted) {
          updateItem(localId, { state: 'stopped', errorMessage: '预检已停止。' })
          return
        }

        if (!canFailOpenPreflight(error)) {
          const presentation = presentPreflightError(error)
          updateItem(localId, {
            state: 'check-failed',
            errorMessage: presentation.message,
            requestId: presentation.requestId,
          })
          return
        }

        const apiError = toApiError(error)
        updateItem(localId, {
          warningMessage: '预检暂时不可用，已改由上传接口完成最终重复检查。',
          warningRequestId: apiError.requestId,
        })
      }

      await uploadAndContinue(localId, currentItem.file, sha256, controller)
    } catch {
      if (controller.signal.aborted) {
        updateItem(localId, { state: 'stopped', errorMessage: '操作已停止。' })
        return
      }
      updateItem(localId, {
        state: 'hash-failed',
        errorMessage: '文件预检未能完成，请重试。',
      })
    } finally {
      if (activeRequestControllers.get(localId) === controller) {
        activeRequestControllers.delete(localId)
      }
    }
  }

  async function uploadWorker(): Promise<void> {
    while (!stopRequested && !disposed) {
      const nextItem = items.value.find((item) => item.state === 'waiting')
      if (!nextItem) return
      updateItem(nextItem.localId, { state: 'hashing' })
      await processItem(nextItem.localId)
    }
  }

  async function start(): Promise<void> {
    if (!canStart.value) return
    stopRequested = false
    isDispatching.value = true
    const waitingCount = items.value.filter((item) => item.state === 'waiting').length
    const workerCount = Math.min(uploadConcurrency, waitingCount)
    try {
      await Promise.all(Array.from({ length: workerCount }, () => uploadWorker()))
    } finally {
      isDispatching.value = false
    }
  }

  function stopRemaining(): void {
    stopRequested = true
    items.value = items.value.map((item) =>
      item.state === 'waiting'
        ? { ...item, state: 'stopped', errorMessage: '尚未开始，已停止。' }
        : item,
    )
    for (const controller of activeRequestControllers.values()) controller.abort()
  }

  async function retryFailed(): Promise<void> {
    if (isDispatching.value) return
    items.value = items.value.map((item) =>
      failedStates.has(item.state)
        ? {
            ...item,
            state: 'waiting',
            job: null,
            sha256: null,
            hashProgress: null,
            errorMessage: '',
            requestId: undefined,
            warningMessage: '',
            warningRequestId: undefined,
          }
        : item,
    )
    await start()
  }

  async function resumeStopped(): Promise<void> {
    if (isDispatching.value) return
    items.value = items.value.map((item) =>
      item.state === 'stopped'
        ? {
            ...item,
            state: 'waiting',
            sha256: null,
            hashProgress: null,
            errorMessage: '',
            requestId: undefined,
            warningMessage: '',
            warningRequestId: undefined,
          }
        : item,
    )
    await start()
  }

  onScopeDispose(() => {
    disposed = true
    if (trackingTimer !== undefined) clearTimeout(trackingTimer)
    for (const controller of activeRequestControllers.values()) controller.abort()
    for (const controller of trackingControllers.values()) controller.abort()
    activeRequestControllers.clear()
    trackingControllers.clear()
    trackedImports.clear()
    fileHashClient.dispose()
  })

  return {
    addFiles,
    canStart,
    canStop,
    clearFinished,
    hasRetryableItems,
    hasStoppedItems,
    isDispatching,
    items,
    removeItem,
    resumeStopped,
    retryFailed,
    selectionMessage,
    start,
    stopRemaining,
    summary,
  }
}
