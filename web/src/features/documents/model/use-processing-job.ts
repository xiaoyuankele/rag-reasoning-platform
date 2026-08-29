import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import {
  isProcessingJobActive,
  type ProcessingJob,
} from '../../../entities/processing-job/model/processing-job'
import { toApiError } from '../../../shared/api/api-error'
import {
  capacityFailureFromApiError,
  type CapacityFailure,
} from '../../../shared/api/capacity-error'
import { useRetryCooldown } from '../../../shared/api/use-retry-cooldown'
import {
  cancelProcessingJob,
  getLatestProcessingJobs,
  getProcessingJob,
  queueDocumentProcessing,
} from '../api/processing-api'

export type ProcessingViewState =
  | 'idle'
  | 'discovering'
  | 'queueing'
  | 'queued'
  | 'processing'
  | 'succeeded'
  | 'failed'
  | 'canceled'
  | 'conflict'
  | 'capacity'
  | 'error'

interface UseProcessingJobOptions {
  pollIntervalMs?: number
  onJobStatusChange?: (job: ProcessingJob) => void | Promise<void>
  onQueueConflict?: () => void | Promise<void>
}

function stateFromJob(job: ProcessingJob): ProcessingViewState {
  return job.status
}

/**
 * 管理一次可观察的解析任务。
 * 递归 setTimeout 会等待上次请求完成后再计时，避免慢请求造成轮询重叠。
 */
export function useProcessingJob(options: UseProcessingJobOptions = {}) {
  const pollIntervalMs = options.pollIntervalMs ?? 1_500
  const state = ref<ProcessingViewState>('idle')
  const job = shallowRef<ProcessingJob | null>(null)
  const errorMessage = ref('')
  const requestId = ref<string | undefined>()
  const capacityFailure = shallowRef<CapacityFailure | null>(null)
  const isCancelling = ref(false)
  const retryCooldown = useRetryCooldown()
  let timer: ReturnType<typeof setTimeout> | undefined
  let requestController: AbortController | null = null
  let operationVersion = 0

  const hasActiveJob = computed(() =>
    job.value === null ? false : isProcessingJobActive(job.value.status),
  )
  const canCancel = computed(
    () => job.value?.status === 'queued' && job.value.cancelable && !isCancelling.value,
  )

  function cancelPendingWork(): void {
    operationVersion += 1
    if (timer !== undefined) {
      clearTimeout(timer)
      timer = undefined
    }
    requestController?.abort()
    requestController = null
  }

  function reset(): void {
    cancelPendingWork()
    state.value = 'idle'
    job.value = null
    errorMessage.value = ''
    requestId.value = undefined
    capacityFailure.value = null
    isCancelling.value = false
    retryCooldown.reset()
  }

  async function applyJob(nextJob: ProcessingJob): Promise<void> {
    const previousStatus = job.value?.status
    job.value = nextJob
    state.value = stateFromJob(nextJob)
    errorMessage.value = ''
    requestId.value = undefined
    capacityFailure.value = null

    if (previousStatus !== nextJob.status) {
      await options.onJobStatusChange?.(nextJob)
    }
  }

  /** 页面进入或发生 409 后，按文档恢复后端最新任务快照。 */
  async function discover(documentId: number): Promise<ProcessingJob | null> {
    cancelPendingWork()
    const version = operationVersion
    const controller = new AbortController()
    requestController = controller
    state.value = 'discovering'
    job.value = null
    errorMessage.value = ''
    requestId.value = undefined
    capacityFailure.value = null

    try {
      const [item] = await getLatestProcessingJobs([documentId], controller.signal)
      if (controller.signal.aborted || version !== operationVersion) return null
      const discoveredJob = item?.job ?? null
      if (discoveredJob === null) {
        state.value = 'idle'
        return null
      }
      await applyJob(discoveredJob)
      schedulePoll(version)
      return discoveredJob
    } catch (error) {
      if (controller.signal.aborted || version !== operationVersion) return null
      const apiError = toApiError(error)
      state.value = 'error'
      errorMessage.value = `无法恢复最近解析任务：${apiError.message}`
      requestId.value = apiError.requestId
      return null
    } finally {
      if (requestController === controller) requestController = null
    }
  }

  function schedulePoll(version: number): void {
    if (!hasActiveJob.value || version !== operationVersion) return
    timer = setTimeout(() => void poll(version), pollIntervalMs)
  }

  async function poll(version: number): Promise<void> {
    const activeJob = job.value
    if (!activeJob || version !== operationVersion) return

    const controller = new AbortController()
    requestController = controller
    try {
      const refreshedJob = await getProcessingJob(activeJob.id, controller.signal)
      if (controller.signal.aborted || version !== operationVersion) return
      await applyJob(refreshedJob)
      schedulePoll(version)
    } catch (error) {
      if (controller.signal.aborted || version !== operationVersion) return
      const apiError = toApiError(error)
      state.value = 'error'
      errorMessage.value = apiError.message
      requestId.value = apiError.requestId
    } finally {
      if (requestController === controller) requestController = null
    }
  }

  async function queue(documentId: number): Promise<boolean> {
    if (retryCooldown.isCoolingDown.value) return false
    cancelPendingWork()
    const version = operationVersion
    const controller = new AbortController()
    requestController = controller
    state.value = 'queueing'
    job.value = null
    errorMessage.value = ''
    requestId.value = undefined
    capacityFailure.value = null

    try {
      const queuedJob = await queueDocumentProcessing(documentId, controller.signal)
      if (controller.signal.aborted || version !== operationVersion) return false
      await applyJob(queuedJob)
      schedulePoll(version)
      return true
    } catch (error) {
      if (controller.signal.aborted || version !== operationVersion) return false
      const apiError = toApiError(error)
      requestId.value = apiError.requestId
      const capacity = capacityFailureFromApiError(apiError, 5)

      if (capacity) {
        capacityFailure.value = capacity
        retryCooldown.start(capacity.retryAfterSeconds)
        state.value = 'capacity'
        errorMessage.value = capacity.message
      } else if (apiError.status === 409) {
        state.value = 'conflict'
        errorMessage.value = '文档当前已有解析任务，或其状态不允许重复创建。'
        await options.onQueueConflict?.()
      } else {
        state.value = 'error'
        errorMessage.value = apiError.message
      }
      return false
    } finally {
      if (requestController === controller) requestController = null
    }
  }

  /** 取消 queued 任务；若 Worker 已抢先领取，则立即回读服务端真实状态。 */
  async function cancel(): Promise<boolean> {
    const activeJob = job.value
    if (!activeJob || !canCancel.value) return false

    cancelPendingWork()
    const version = operationVersion
    const controller = new AbortController()
    requestController = controller
    isCancelling.value = true
    errorMessage.value = ''
    requestId.value = undefined

    try {
      const canceledJob = await cancelProcessingJob(activeJob.id, controller.signal)
      if (controller.signal.aborted || version !== operationVersion) return false
      await applyJob(canceledJob)
      return true
    } catch (error) {
      if (controller.signal.aborted || version !== operationVersion) return false
      const apiError = toApiError(error)
      requestId.value = apiError.requestId

      if (apiError.status === 409) {
        try {
          const refreshedJob = await getProcessingJob(activeJob.id, controller.signal)
          if (controller.signal.aborted || version !== operationVersion) return false
          await applyJob(refreshedJob)
          schedulePoll(version)
        } catch (refreshError) {
          if (controller.signal.aborted || version !== operationVersion) return false
          const refreshApiError = toApiError(refreshError)
          state.value = 'conflict'
          errorMessage.value = '任务状态已经变化，暂时无法确认最新状态。'
          requestId.value = refreshApiError.requestId ?? apiError.requestId
          schedulePoll(version)
        }
      } else {
        state.value = 'error'
        errorMessage.value = apiError.message
      }
      return false
    } finally {
      if (requestController === controller) requestController = null
      isCancelling.value = false
    }
  }

  function resumePolling(): void {
    if (!job.value || !hasActiveJob.value) return
    cancelPendingWork()
    state.value = stateFromJob(job.value)
    errorMessage.value = ''
    requestId.value = undefined
    schedulePoll(operationVersion)
  }

  onScopeDispose(cancelPendingWork)

  return {
    canCancel,
    cancel,
    capacityFailure,
    discover,
    errorMessage,
    hasActiveJob,
    isCancelling,
    isCoolingDown: retryCooldown.isCoolingDown,
    job,
    queue,
    requestId,
    reset,
    retryAfterSeconds: retryCooldown.remainingSeconds,
    resumePolling,
    state,
  }
}
