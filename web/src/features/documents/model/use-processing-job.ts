import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import type { ProcessingJob } from '../../../entities/processing-job/model/processing-job'
import { toApiError } from '../../../shared/api/api-error'
import { getProcessingJob, queueDocumentProcessing } from '../api/processing-api'

export type ProcessingViewState =
  'idle' | 'queueing' | 'queued' | 'processing' | 'succeeded' | 'failed' | 'conflict' | 'error'

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
  let timer: ReturnType<typeof setTimeout> | undefined
  let requestController: AbortController | null = null
  let operationVersion = 0

  const hasActiveJob = computed(
    () => job.value?.status === 'queued' || job.value?.status === 'processing',
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
  }

  async function applyJob(nextJob: ProcessingJob): Promise<void> {
    const previousStatus = job.value?.status
    job.value = nextJob
    state.value = stateFromJob(nextJob)
    errorMessage.value = ''
    requestId.value = undefined

    if (previousStatus !== nextJob.status) {
      await options.onJobStatusChange?.(nextJob)
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
    cancelPendingWork()
    const version = operationVersion
    const controller = new AbortController()
    requestController = controller
    state.value = 'queueing'
    job.value = null
    errorMessage.value = ''
    requestId.value = undefined

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

      if (apiError.status === 409) {
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
    errorMessage,
    hasActiveJob,
    job,
    queue,
    requestId,
    reset,
    resumePolling,
    state,
  }
}
