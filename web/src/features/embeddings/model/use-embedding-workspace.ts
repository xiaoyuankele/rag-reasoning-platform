import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import type { EmbeddingJob } from '../../../entities/embedding-job/model/embedding-job'
import {
  canCancelEmbeddingJob,
  isEmbeddingJobActive,
} from '../../../entities/embedding-job/model/embedding-job'
import type { ResearchDocument } from '../../../entities/document/model/document'
import type { DocumentPage } from '../../../entities/document/model/document'
import { toApiError } from '../../../shared/api/api-error'
import {
  cancelEmbeddingJob,
  getEmbeddingJob,
  queueEmbeddingJob,
  queueEmbeddingJobs,
} from '../api/embedding-api'

export type EmbeddingWorkspaceState = 'idle' | 'loading' | 'success' | 'empty' | 'error'
export type EmbeddingDocumentAction = 'submitting' | 'cancelling'

export interface EmbeddingDocumentFeedback {
  kind: 'success' | 'error'
  message: string
  requestId?: string
}

export interface EmbeddingWorkspaceOptions {
  loadDocumentPage: EmbeddingDocumentPageLoader
  pollIntervalMs?: number
  pollConcurrency?: number
  storage?: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null
}

/** 由页面组合层注入文档分页能力，避免 embeddings feature 反向依赖 documents feature。 */
export type EmbeddingDocumentPageLoader = (
  params: { page: number; pageSize: number },
  signal?: AbortSignal,
) => Promise<DocumentPage>

const documentPageSize = 100
const maximumBatchSize = 100
const storedJobsKey = 'rag.embedding-jobs.v1'

function defaultSessionStorage(): Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null {
  if (typeof window === 'undefined') return null
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

function readStoredJobs(
  storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null,
): Map<number, number> {
  if (!storage) return new Map()
  try {
    const value: unknown = JSON.parse(storage.getItem(storedJobsKey) ?? '{}')
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return new Map()

    const jobs = new Map<number, number>()
    for (const [rawDocumentId, rawJobId] of Object.entries(value)) {
      const documentId = Number(rawDocumentId)
      if (
        Number.isSafeInteger(documentId) &&
        documentId > 0 &&
        typeof rawJobId === 'number' &&
        Number.isSafeInteger(rawJobId) &&
        rawJobId > 0
      ) {
        jobs.set(documentId, rawJobId)
      }
    }
    return jobs
  } catch {
    return new Map()
  }
}

function writeStoredJobs(
  storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null,
  jobs: Map<number, number>,
): void {
  if (!storage) return
  try {
    if (jobs.size === 0) {
      storage.removeItem(storedJobsKey)
      return
    }
    storage.setItem(storedJobsKey, JSON.stringify(Object.fromEntries(jobs)))
  } catch {
    // 浏览器禁用会话存储时不影响后端任务正确性，只失去刷新恢复能力。
  }
}

/**
 * 管理独立向量化页面的数据流。
 * 文档列表、批量命令、集中轮询和会话恢复都停留在本模块，不侵入文档库页面。
 */
export function useEmbeddingWorkspace(options: EmbeddingWorkspaceOptions) {
  const pollIntervalMs = options.pollIntervalMs ?? 2_000
  const pollConcurrency = Math.max(1, options.pollConcurrency ?? 4)
  const storage = options.storage === undefined ? defaultSessionStorage() : options.storage

  const state = ref<EmbeddingWorkspaceState>('idle')
  const documents = shallowRef<ResearchDocument[]>([])
  const selectedDocumentIds = shallowRef<Set<number>>(new Set())
  const jobsByDocumentId = shallowRef<Map<number, EmbeddingJob>>(new Map())
  const actionsByDocumentId = shallowRef<Map<number, EmbeddingDocumentAction>>(new Map())
  const feedbackByDocumentId = shallowRef<Map<number, EmbeddingDocumentFeedback>>(new Map())
  const workspaceMessage = ref<EmbeddingDocumentFeedback | null>(null)
  const requestId = ref<string | undefined>()

  const storedJobs = readStoredJobs(storage)
  const activeControllers = new Set<AbortController>()
  let pollTimer: ReturnType<typeof setTimeout> | null = null
  let polling = false
  let initialized = false
  let disposed = false

  const selectedCount = computed(() => selectedDocumentIds.value.size)
  const activeJobCount = computed(
    () =>
      [...jobsByDocumentId.value.values()].filter((job) => isEmbeddingJobActive(job.status)).length,
  )
  const isSubmitting = computed(() =>
    [...actionsByDocumentId.value.values()].some((action) => action === 'submitting'),
  )

  function replaceMapValue<K, V>(source: Map<K, V>, key: K, value: V): Map<K, V> {
    const next = new Map(source)
    next.set(key, value)
    return next
  }

  function removeMapValue<K, V>(source: Map<K, V>, key: K): Map<K, V> {
    const next = new Map(source)
    next.delete(key)
    return next
  }

  function setAction(documentId: number, action: EmbeddingDocumentAction | null): void {
    actionsByDocumentId.value = action
      ? replaceMapValue(actionsByDocumentId.value, documentId, action)
      : removeMapValue(actionsByDocumentId.value, documentId)
  }

  function setFeedback(documentId: number, feedback: EmbeddingDocumentFeedback | null): void {
    feedbackByDocumentId.value = feedback
      ? replaceMapValue(feedbackByDocumentId.value, documentId, feedback)
      : removeMapValue(feedbackByDocumentId.value, documentId)
  }

  function rememberJob(job: EmbeddingJob): void {
    jobsByDocumentId.value = replaceMapValue(jobsByDocumentId.value, job.documentId, job)
    storedJobs.set(job.documentId, job.id)
    writeStoredJobs(storage, storedJobs)
  }

  function forgetJob(documentId: number): void {
    jobsByDocumentId.value = removeMapValue(jobsByDocumentId.value, documentId)
    storedJobs.delete(documentId)
    writeStoredJobs(storage, storedJobs)
  }

  function createController(): AbortController {
    const controller = new AbortController()
    activeControllers.add(controller)
    return controller
  }

  function releaseController(controller: AbortController): void {
    activeControllers.delete(controller)
  }

  async function loadAllDocuments(signal: AbortSignal): Promise<ResearchDocument[]> {
    const foundDocuments = new Map<number, ResearchDocument>()
    let page = 1
    let totalPages = 1

    do {
      const result = await options.loadDocumentPage({ page, pageSize: documentPageSize }, signal)
      for (const document of result.documents) foundDocuments.set(document.id, document)
      totalPages = Math.max(1, result.pagination.totalPages)
      page += 1
    } while (page <= totalPages && !signal.aborted)

    return [...foundDocuments.values()]
  }

  async function runInBatches<T>(
    values: T[],
    callback: (value: T) => Promise<void>,
  ): Promise<void> {
    for (let index = 0; index < values.length; index += pollConcurrency) {
      if (disposed) return
      await Promise.all(values.slice(index, index + pollConcurrency).map(callback))
    }
  }

  async function restoreKnownJobs(signal: AbortSignal): Promise<void> {
    const visibleDocumentIds = new Set(documents.value.map((document) => document.id))
    const entries = [...storedJobs.entries()].filter(([documentId]) =>
      visibleDocumentIds.has(documentId),
    )

    await runInBatches(entries, async ([documentId, jobId]) => {
      try {
        const job = await getEmbeddingJob(jobId, signal)
        if (job.documentId !== documentId) {
          forgetJob(documentId)
          return
        }
        rememberJob(job)
      } catch (error) {
        if (signal.aborted) return
        const apiError = toApiError(error)
        if (apiError.kind === 'not-found') {
          forgetJob(documentId)
          return
        }
        setFeedback(documentId, {
          kind: 'error',
          message: `暂时无法恢复任务状态：${apiError.message}`,
          requestId: apiError.requestId,
        })
      }
    })
  }

  /** 重新读取全部文档，并恢复当前浏览器会话知道的最近任务。 */
  async function load(): Promise<void> {
    const controller = createController()
    state.value = 'loading'
    requestId.value = undefined
    workspaceMessage.value = null

    try {
      const result = await loadAllDocuments(controller.signal)
      if (controller.signal.aborted || disposed) return
      documents.value = result
      state.value = result.length === 0 ? 'empty' : 'success'
      await restoreKnownJobs(controller.signal)
      schedulePoll()
    } catch (error) {
      if (controller.signal.aborted || disposed) return
      const apiError = toApiError(error)
      documents.value = []
      state.value = 'error'
      workspaceMessage.value = { kind: 'error', message: apiError.message }
      requestId.value = apiError.requestId
    } finally {
      releaseController(controller)
    }
  }

  function toggleDocument(documentId: number, selected: boolean): void {
    const next = new Set(selectedDocumentIds.value)
    if (!selected) {
      next.delete(documentId)
    } else if (next.size < maximumBatchSize || next.has(documentId)) {
      next.add(documentId)
    } else {
      workspaceMessage.value = {
        kind: 'error',
        message: `单次最多选择 ${maximumBatchSize} 份文档。`,
      }
    }
    selectedDocumentIds.value = next
  }

  function selectDocuments(documentIds: number[]): void {
    const next = new Set(selectedDocumentIds.value)
    for (const documentId of documentIds) {
      if (next.size >= maximumBatchSize) break
      next.add(documentId)
    }
    selectedDocumentIds.value = next
    if (documentIds.some((documentId) => !next.has(documentId))) {
      workspaceMessage.value = {
        kind: 'error',
        message: `已达到单次 ${maximumBatchSize} 份文档的上限。`,
      }
    }
  }

  function clearSelection(): void {
    selectedDocumentIds.value = new Set()
  }

  function clearSuccessfulSelections(successfulIds: number[]): void {
    const next = new Set(selectedDocumentIds.value)
    for (const documentId of successfulIds) next.delete(documentId)
    selectedDocumentIds.value = next
  }

  /** 单篇走幂等接口，多篇走逐项返回的批量接口。 */
  async function queueSelected(): Promise<void> {
    const documentIds = [...selectedDocumentIds.value]
    if (documentIds.length === 0 || isSubmitting.value) {
      if (documentIds.length === 0) {
        workspaceMessage.value = { kind: 'error', message: '请先选择需要向量化的文档。' }
      }
      return
    }

    const controller = createController()
    workspaceMessage.value = null
    for (const documentId of documentIds) {
      setAction(documentId, 'submitting')
      setFeedback(documentId, null)
    }

    try {
      if (documentIds.length === 1) {
        const documentId = documentIds[0]
        const result = await queueEmbeddingJob(documentId, controller.signal)
        if (controller.signal.aborted || disposed) return
        rememberJob(result.job)
        setFeedback(documentId, {
          kind: 'success',
          message: result.created ? '向量任务已创建。' : '已有活动任务，已恢复跟踪。',
        })
        clearSuccessfulSelections([documentId])
        workspaceMessage.value = { kind: 'success', message: '已提交 1 份文档。' }
      } else {
        const results = await queueEmbeddingJobs(documentIds, controller.signal)
        if (controller.signal.aborted || disposed) return
        const successfulIds: number[] = []
        let created = 0
        let alreadyActive = 0

        for (const item of results) {
          if (item.job) {
            rememberJob(item.job)
            successfulIds.push(item.documentId)
            if (item.outcome === 'created') created += 1
            if (item.outcome === 'already_active') alreadyActive += 1
            setFeedback(item.documentId, {
              kind: 'success',
              message:
                item.outcome === 'created' ? '向量任务已创建。' : '已有活动任务，已恢复跟踪。',
            })
          } else {
            setFeedback(item.documentId, {
              kind: 'error',
              message: item.errorMessage ?? '该文档未能创建向量任务。',
            })
          }
        }

        clearSuccessfulSelections(successfulIds)
        workspaceMessage.value = {
          kind: results.length === successfulIds.length ? 'success' : 'error',
          message: `批量处理完成：新建 ${created}，复用 ${alreadyActive}，失败 ${results.length - successfulIds.length}。`,
        }
      }
      schedulePoll()
    } catch (error) {
      if (controller.signal.aborted || disposed) return
      const apiError = toApiError(error)
      workspaceMessage.value = {
        kind: 'error',
        message: apiError.message,
        requestId: apiError.requestId,
      }
    } finally {
      for (const documentId of documentIds) setAction(documentId, null)
      releaseController(controller)
    }
  }

  /** 请求取消后使用服务端返回的新状态覆盖本地状态。 */
  async function cancel(documentId: number): Promise<void> {
    const job = jobsByDocumentId.value.get(documentId)
    if (!job || !canCancelEmbeddingJob(job.status) || actionsByDocumentId.value.has(documentId)) {
      return
    }

    const controller = createController()
    setAction(documentId, 'cancelling')
    setFeedback(documentId, null)
    try {
      const canceledJob = await cancelEmbeddingJob(job.id, controller.signal)
      if (controller.signal.aborted || disposed) return
      rememberJob(canceledJob)
      setFeedback(documentId, { kind: 'success', message: '向量任务已取消。' })
    } catch (error) {
      if (controller.signal.aborted || disposed) return
      const apiError = toApiError(error)
      setFeedback(documentId, {
        kind: 'error',
        message:
          apiError.kind === 'conflict'
            ? '任务状态已变化，正在重新获取最新状态。'
            : apiError.message,
        requestId: apiError.requestId,
      })
      if (apiError.kind === 'conflict') schedulePoll(0)
    } finally {
      setAction(documentId, null)
      releaseController(controller)
    }
  }

  async function pollActiveJobs(): Promise<void> {
    if (disposed || polling) return
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return

    const activeJobs = [...jobsByDocumentId.value.values()].filter((job) =>
      isEmbeddingJobActive(job.status),
    )
    if (activeJobs.length === 0) return

    polling = true
    const controller = createController()
    try {
      await runInBatches(activeJobs, async (knownJob) => {
        try {
          const latestJob = await getEmbeddingJob(knownJob.id, controller.signal)
          if (controller.signal.aborted || disposed) return
          rememberJob(latestJob)
          if (latestJob.status === 'succeeded') {
            setFeedback(latestJob.documentId, {
              kind: 'success',
              message: '向量化完成，现在可以用于语义检索。',
            })
          } else if (latestJob.status === 'failed') {
            setFeedback(latestJob.documentId, {
              kind: 'error',
              message: latestJob.errorMessage ?? '向量化失败，请稍后重新申请。',
            })
          }
        } catch (error) {
          if (controller.signal.aborted || disposed) return
          const apiError = toApiError(error)
          if (apiError.kind === 'not-found') {
            forgetJob(knownJob.documentId)
          } else {
            setFeedback(knownJob.documentId, {
              kind: 'error',
              message: `任务状态更新失败：${apiError.message}`,
              requestId: apiError.requestId,
            })
          }
        }
      })
    } finally {
      polling = false
      releaseController(controller)
      schedulePoll()
    }
  }

  function schedulePoll(delay = pollIntervalMs): void {
    if (pollTimer !== null) clearTimeout(pollTimer)
    pollTimer = null
    if (disposed || activeJobCount.value === 0) return
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    pollTimer = setTimeout(() => void pollActiveJobs(), delay)
  }

  function handleVisibilityChange(): void {
    if (typeof document !== 'undefined' && document.visibilityState === 'visible') schedulePoll(0)
  }

  async function initialize(): Promise<void> {
    if (initialized) return
    initialized = true
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', handleVisibilityChange)
    }
    await load()
  }

  function dispose(): void {
    if (disposed) return
    disposed = true
    if (pollTimer !== null) clearTimeout(pollTimer)
    for (const controller of activeControllers) controller.abort()
    activeControllers.clear()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }

  onScopeDispose(dispose)

  return {
    actionsByDocumentId,
    activeJobCount,
    cancel,
    clearSelection,
    documents,
    feedbackByDocumentId,
    initialize,
    isSubmitting,
    jobsByDocumentId,
    load,
    queueSelected,
    requestId,
    selectDocuments,
    selectedCount,
    selectedDocumentIds,
    state,
    toggleDocument,
    workspaceMessage,
  }
}

export { maximumBatchSize, storedJobsKey }
