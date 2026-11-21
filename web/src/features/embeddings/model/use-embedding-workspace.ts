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
  capacityFailureFromApiError,
  createCapacityFailure,
  type CapacityFailure,
} from '../../../shared/api/capacity-error'
import { useRetryCooldown } from '../../../shared/api/use-retry-cooldown'
import {
  cancelEmbeddingJob,
  getEmbeddingJob,
  getLatestEmbeddingJobs,
  queueEmbeddingJob,
  queueEmbeddingJobs,
} from '../api/embedding-api'

export type EmbeddingWorkspaceState = 'idle' | 'loading' | 'success' | 'empty' | 'error'
export type EmbeddingDocumentAction = 'submitting' | 'cancelling'

export interface EmbeddingDocumentFeedback {
  kind: 'success' | 'error' | 'capacity'
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
  const discoveredDocumentIds = shallowRef<Set<number>>(new Set())
  const actionsByDocumentId = shallowRef<Map<number, EmbeddingDocumentAction>>(new Map())
  const feedbackByDocumentId = shallowRef<Map<number, EmbeddingDocumentFeedback>>(new Map())
  const workspaceMessage = ref<EmbeddingDocumentFeedback | null>(null)
  const requestId = ref<string | undefined>()
  const capacityFailure = ref<CapacityFailure | null>(null)
  const retryCooldown = useRetryCooldown()

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

  function activateCapacityFailure(failure: CapacityFailure): void {
    capacityFailure.value = failure
    requestId.value = failure.requestId
    retryCooldown.start(failure.retryAfterSeconds)
  }

  function clearCapacityFailure(): void {
    capacityFailure.value = null
    retryCooldown.reset()
  }

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
    discoveredDocumentIds.value = new Set(discoveredDocumentIds.value).add(job.documentId)
    storedJobs.set(job.documentId, job.id)
    writeStoredJobs(storage, storedJobs)
  }

  function rememberNoJob(documentId: number): void {
    jobsByDocumentId.value = removeMapValue(jobsByDocumentId.value, documentId)
    discoveredDocumentIds.value = new Set(discoveredDocumentIds.value).add(documentId)
    storedJobs.delete(documentId)
    writeStoredJobs(storage, storedJobs)
  }

  function forgetJob(documentId: number): void {
    jobsByDocumentId.value = removeMapValue(jobsByDocumentId.value, documentId)
    const nextDiscoveredIds = new Set(discoveredDocumentIds.value)
    nextDiscoveredIds.delete(documentId)
    discoveredDocumentIds.value = nextDiscoveredIds
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

  async function discoverLatestJobs(signal: AbortSignal): Promise<void> {
    const documentIds = documents.value.map((document) => document.id)
    for (let index = 0; index < documentIds.length; index += maximumBatchSize) {
      const items = await getLatestEmbeddingJobs(
        documentIds.slice(index, index + maximumBatchSize),
        signal,
      )
      if (signal.aborted || disposed) return
      for (const item of items) {
        if (item.job) rememberJob(item.job)
        else rememberNoJob(item.documentId)
      }
    }
  }

  /** 重新读取全部文档，并从后端批量恢复每篇文档的最新任务快照。 */
  async function load(): Promise<void> {
    const controller = createController()
    state.value = 'loading'
    requestId.value = undefined
    workspaceMessage.value = null

    try {
      const result = await loadAllDocuments(controller.signal)
      if (controller.signal.aborted || disposed) return
      documents.value = result
      jobsByDocumentId.value = new Map()
      discoveredDocumentIds.value = new Set()
      if (result.length > 0) {
        try {
          await discoverLatestJobs(controller.signal)
        } catch (error) {
          if (controller.signal.aborted || disposed) return
          const apiError = toApiError(error)
          workspaceMessage.value = {
            kind: 'error',
            message: `文档已加载，但向量任务状态恢复失败：${apiError.message}`,
            requestId: apiError.requestId,
          }
        }
      }
      state.value = result.length === 0 ? 'empty' : 'success'
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

  /**
   * 单篇走幂等接口，多篇按后端上限顺序拆批。
   * 顺序拆批可以避免一次制造过多请求，同时允许“全部文档”超过单批 100 份。
   */
  async function queueDocuments(documentIds: number[], submissionLabel: string): Promise<void> {
    const uniqueDocumentIds = [...new Set(documentIds)]
    if (uniqueDocumentIds.length === 0 || isSubmitting.value || retryCooldown.isCoolingDown.value) {
      return
    }

    const controller = createController()
    clearCapacityFailure()
    workspaceMessage.value = null
    for (const documentId of uniqueDocumentIds) {
      setAction(documentId, 'submitting')
      setFeedback(documentId, null)
    }

    const successfulIds: number[] = []
    let created = 0
    let alreadyActive = 0
    let failed = 0
    let deferred = 0
    let completed = 0
    let blockedByCapacity: CapacityFailure | null = null

    try {
      if (uniqueDocumentIds.length === 1) {
        const documentId = uniqueDocumentIds[0]
        const result = await queueEmbeddingJob(documentId, controller.signal)
        if (controller.signal.aborted || disposed) return
        rememberJob(result.job)
        successfulIds.push(documentId)
        created += result.created ? 1 : 0
        alreadyActive += result.created ? 0 : 1
        completed = 1
        setFeedback(documentId, {
          kind: 'success',
          message: result.created ? '向量任务已创建。' : '已有活动任务，已恢复跟踪。',
        })
      } else {
        for (let index = 0; index < uniqueDocumentIds.length; index += maximumBatchSize) {
          const batchIds = uniqueDocumentIds.slice(index, index + maximumBatchSize)
          const batch = await queueEmbeddingJobs(batchIds, controller.signal)
          if (controller.signal.aborted || disposed) return

          for (const item of batch.items) {
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
              const itemCapacity = createCapacityFailure(
                item.errorCode,
                batch.retryAfterSeconds,
                batch.requestId,
                5,
              )
              if (itemCapacity) {
                blockedByCapacity ??= itemCapacity
                deferred += 1
                setFeedback(item.documentId, {
                  kind: 'capacity',
                  message: `${itemCapacity.title}。${itemCapacity.message}`,
                  requestId: itemCapacity.requestId,
                })
                continue
              }

              failed += 1
              setFeedback(item.documentId, {
                kind: 'error',
                message: item.errorMessage ?? '该文档未能创建向量任务。',
              })
            }
          }

          completed += batch.items.length
          clearSuccessfulSelections(successfulIds)
          if (blockedByCapacity) {
            activateCapacityFailure(blockedByCapacity)
            break
          }
        }
      }

      clearSuccessfulSelections(successfulIds)
      if (blockedByCapacity) {
        const remaining = uniqueDocumentIds.length - successfulIds.length
        workspaceMessage.value = {
          kind: 'capacity',
          message: `${blockedByCapacity.title}。本次已处理 ${completed}/${uniqueDocumentIds.length} 份，新建 ${created}，复用 ${alreadyActive}，普通失败 ${failed}，容量暂缓 ${deferred}；仍有 ${remaining} 份未成功提交。${blockedByCapacity.message}`,
          requestId: blockedByCapacity.requestId,
        }
      } else {
        workspaceMessage.value = {
          kind: failed === 0 ? 'success' : 'error',
          message: `${submissionLabel}处理完成：新建 ${created}，复用 ${alreadyActive}，失败 ${failed}。`,
        }
      }
      schedulePoll()
    } catch (error) {
      if (controller.signal.aborted || disposed) return
      const apiError = toApiError(error)
      const capacity = capacityFailureFromApiError(apiError, 5)
      if (capacity) {
        activateCapacityFailure(capacity)
        const successfulSet = new Set(successfulIds)
        for (const documentId of uniqueDocumentIds) {
          if (successfulSet.has(documentId)) continue
          setFeedback(documentId, {
            kind: 'capacity',
            message: `${capacity.title}。${capacity.message}`,
            requestId: capacity.requestId,
          })
        }
        workspaceMessage.value = {
          kind: 'capacity',
          message:
            completed > 0
              ? `${capacity.title}。已处理 ${completed}/${uniqueDocumentIds.length} 份；${capacity.message}`
              : `${capacity.title}。${capacity.message}`,
          requestId: capacity.requestId,
        }
      } else {
        workspaceMessage.value = {
          kind: 'error',
          message:
            completed > 0
              ? `已处理 ${completed}/${uniqueDocumentIds.length} 份文档，后续批次提交失败：${apiError.message}`
              : apiError.message,
          requestId: apiError.requestId,
        }
      }
      if (successfulIds.length > 0) schedulePoll()
    } finally {
      for (const documentId of uniqueDocumentIds) setAction(documentId, null)
      releaseController(controller)
    }
  }

  async function queueSelected(): Promise<void> {
    const documentIds = [...selectedDocumentIds.value]
    if (documentIds.length === 0) {
      workspaceMessage.value = { kind: 'error', message: '请先选择需要向量化的文档。' }
      return
    }
    await queueDocuments(documentIds, documentIds.length === 1 ? '' : '批量')
  }

  async function queueAll(documentIds: number[]): Promise<void> {
    if (documentIds.length === 0) {
      workspaceMessage.value = { kind: 'error', message: '当前没有可提交的文档。' }
      return
    }
    await queueDocuments(documentIds, '全部文档')
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
              message: '最近向量任务已成功；当前文档版本是否就绪仍以后续版本契约为准。',
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
    capacityFailure,
    clearSelection,
    discoveredDocumentIds,
    documents,
    feedbackByDocumentId,
    initialize,
    isCoolingDown: retryCooldown.isCoolingDown,
    isSubmitting,
    jobsByDocumentId,
    load,
    queueAll,
    queueSelected,
    requestId,
    retryAfterSeconds: retryCooldown.remainingSeconds,
    selectDocuments,
    selectedCount,
    selectedDocumentIds,
    state,
    toggleDocument,
    workspaceMessage,
  }
}

export { maximumBatchSize, storedJobsKey }
