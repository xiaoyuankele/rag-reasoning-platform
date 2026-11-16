import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import type { AgentRun } from '../../../entities/agent/model/agent-run'
import { isAgentRunActive } from '../../../entities/agent/model/agent-run'
import { toApiError } from '../../../shared/api/api-error'
import {
  capacityFailureFromApiError,
  type CapacityFailure,
} from '../../../shared/api/capacity-error'
import { useRetryCooldown } from '../../../shared/api/use-retry-cooldown'
import {
  cancelAgentRun,
  createAgentRun,
  getActiveAgentRun,
  getAgentRun,
  getLatestAgentRun,
  isAgentCapabilityUnavailable,
  type AgentRunStartResult,
  type CreateAgentRunParams,
} from '../api/agent-api'

export type AgentWorkspaceState =
  'idle' | 'restoring' | 'queueing' | 'polling' | 'canceling' | 'terminal'
export type AgentAvailability = 'unknown' | 'available' | 'unavailable'

export interface AgentConversationContext {
  conversationId: number
  selectionGeneration: number
}

export type AgentRestoreOutcome =
  | { kind: 'active'; run: AgentRun }
  | { kind: 'terminal'; run: AgentRun }
  | { kind: 'empty' }
  | { kind: 'unavailable' }
  | { kind: 'failed' }
  | { kind: 'stale' }

export type AgentQueueOutcome =
  | { kind: 'started'; result: AgentRunStartResult }
  | { kind: 'execution-conflict' }
  | { kind: 'capacity' }
  | { kind: 'unavailable' }
  | { kind: 'failed' }
  | { kind: 'stale' }

export type AgentCancelOutcome =
  | { kind: 'canceled'; run: AgentRun }
  | { kind: 'refreshed'; run: AgentRun }
  | { kind: 'failed' }
  | { kind: 'stale' }

export interface UseAgentRunWorkspaceOptions {
  pollIntervalMs?: number
  isSelectionCurrent?: (context: AgentConversationContext) => boolean
  onTerminal?: (context: AgentConversationContext, run: AgentRun) => void | Promise<void>
}

/**
 * 管理单个 conversation 的 Agent Run 恢复、轮询和取消。
 * PostgreSQL/API 仍是任务事实源；本模块不写入任何浏览器存储。
 */
export function useAgentRunWorkspace(options: UseAgentRunWorkspaceOptions = {}) {
  const pollIntervalMs = options.pollIntervalMs ?? 1_500
  const state = ref<AgentWorkspaceState>('idle')
  const availability = ref<AgentAvailability>('unknown')
  const run = shallowRef<AgentRun | null>(null)
  const errorMessage = ref('')
  const requestId = ref<string | undefined>()
  const capacityFailure = ref<CapacityFailure | null>(null)
  const manualRestoreRequired = ref(false)
  const retryCooldown = useRetryCooldown()

  let context: AgentConversationContext | null = null
  let contextGeneration = 0
  let actionGeneration = 0
  let pollGeneration = 0
  let pollTimer: ReturnType<typeof setTimeout> | null = null
  let disposed = false
  let notifiedTerminalKey = ''
  const controllers = new Set<AbortController>()

  const isActive = computed(() => run.value !== null && isAgentRunActive(run.value.status))
  const isBusy = computed(
    () =>
      state.value === 'restoring' ||
      state.value === 'queueing' ||
      state.value === 'polling' ||
      state.value === 'canceling',
  )
  const canCancel = computed(() => run.value?.cancelable === true && state.value !== 'canceling')
  const canSubmit = computed(
    () =>
      availability.value !== 'unavailable' &&
      !isActive.value &&
      state.value !== 'restoring' &&
      state.value !== 'queueing' &&
      state.value !== 'canceling' &&
      !retryCooldown.isCoolingDown.value,
  )

  function selectionIsCurrent(candidate: AgentConversationContext): boolean {
    return options.isSelectionCurrent?.(candidate) ?? true
  }

  function contextIsCurrent(candidate: AgentConversationContext, generation: number): boolean {
    return (
      !disposed &&
      generation === contextGeneration &&
      context?.conversationId === candidate.conversationId &&
      context.selectionGeneration === candidate.selectionGeneration &&
      selectionIsCurrent(candidate)
    )
  }

  function actionIsCurrent(
    candidate: AgentConversationContext,
    candidateContextGeneration: number,
    candidateActionGeneration: number,
  ): boolean {
    return (
      contextIsCurrent(candidate, candidateContextGeneration) &&
      candidateActionGeneration === actionGeneration
    )
  }

  function createController(): AbortController {
    const controller = new AbortController()
    controllers.add(controller)
    return controller
  }

  function releaseController(controller: AbortController): void {
    controllers.delete(controller)
  }

  function abortControllers(): void {
    for (const controller of controllers) controller.abort()
    controllers.clear()
  }

  function invalidatePolling(): void {
    pollGeneration += 1
    if (pollTimer !== null) clearTimeout(pollTimer)
    pollTimer = null
  }

  function clearRequestFeedback(): void {
    errorMessage.value = ''
    requestId.value = undefined
    manualRestoreRequired.value = false
  }

  function clearCapacityFailure(): void {
    capacityFailure.value = null
    retryCooldown.reset()
  }

  function markUnavailable(): void {
    invalidatePolling()
    availability.value = 'unavailable'
    run.value = null
    state.value = 'idle'
    manualRestoreRequired.value = false
    errorMessage.value = ''
    requestId.value = undefined
  }

  function bindConversation(nextContext: AgentConversationContext): void {
    contextGeneration += 1
    actionGeneration += 1
    invalidatePolling()
    abortControllers()
    context = { ...nextContext }
    run.value = null
    state.value = 'idle'
    notifiedTerminalKey = ''
    clearRequestFeedback()
  }

  function clearConversation(): void {
    contextGeneration += 1
    actionGeneration += 1
    invalidatePolling()
    abortControllers()
    context = null
    run.value = null
    state.value = 'idle'
    notifiedTerminalKey = ''
    clearRequestFeedback()
  }

  async function notifyTerminal(
    candidate: AgentConversationContext,
    candidateContextGeneration: number,
    terminalRun: AgentRun,
  ): Promise<void> {
    const key = `${terminalRun.id}:${terminalRun.status}:${terminalRun.completedAt?.getTime() ?? 0}`
    if (key === notifiedTerminalKey || !contextIsCurrent(candidate, candidateContextGeneration))
      return
    try {
      await options.onTerminal?.(candidate, terminalRun)
      if (contextIsCurrent(candidate, candidateContextGeneration)) notifiedTerminalKey = key
    } catch {
      if (!contextIsCurrent(candidate, candidateContextGeneration)) return
      manualRestoreRequired.value = true
      errorMessage.value = '最终消息刷新失败，请重新读取状态。'
    }
  }

  function applyRun(
    candidate: AgentConversationContext,
    candidateContextGeneration: number,
    nextRun: AgentRun,
  ): boolean {
    if (
      !contextIsCurrent(candidate, candidateContextGeneration) ||
      nextRun.conversationId !== candidate.conversationId
    ) {
      return false
    }
    availability.value = 'available'
    run.value = nextRun
    manualRestoreRequired.value = false
    errorMessage.value = ''
    requestId.value = undefined
    state.value = isAgentRunActive(nextRun.status) ? 'polling' : 'terminal'
    return true
  }

  function startPolling(
    candidate: AgentConversationContext,
    candidateContextGeneration: number,
    nextRun: AgentRun,
  ): void {
    invalidatePolling()
    const candidatePollGeneration = pollGeneration
    run.value = nextRun
    state.value = 'polling'
    void poll(candidate, candidateContextGeneration, candidatePollGeneration, nextRun.id)
  }

  async function poll(
    candidate: AgentConversationContext,
    candidateContextGeneration: number,
    candidatePollGeneration: number,
    runId: number,
  ): Promise<void> {
    if (
      !contextIsCurrent(candidate, candidateContextGeneration) ||
      candidatePollGeneration !== pollGeneration
    ) {
      return
    }
    const controller = createController()
    try {
      const nextRun = await getAgentRun(runId, controller.signal)
      if (
        !contextIsCurrent(candidate, candidateContextGeneration) ||
        candidatePollGeneration !== pollGeneration
      ) {
        return
      }
      if (!applyRun(candidate, candidateContextGeneration, nextRun)) return
      if (isAgentRunActive(nextRun.status)) {
        pollTimer = setTimeout(() => {
          void poll(candidate, candidateContextGeneration, candidatePollGeneration, runId)
        }, pollIntervalMs)
        return
      }
      pollTimer = null
      await notifyTerminal(candidate, candidateContextGeneration, nextRun)
    } catch (error) {
      if (
        !contextIsCurrent(candidate, candidateContextGeneration) ||
        candidatePollGeneration !== pollGeneration
      ) {
        return
      }
      pollTimer = null
      const apiError = toApiError(error)
      if (isAgentCapabilityUnavailable(apiError)) {
        markUnavailable()
        return
      }
      state.value = 'idle'
      manualRestoreRequired.value = true
      errorMessage.value = apiError.message
      requestId.value = apiError.requestId
    } finally {
      releaseController(controller)
    }
  }

  async function restore(mode: 'active' | 'latest'): Promise<AgentRestoreOutcome> {
    const candidate = context
    if (!candidate) return { kind: 'failed' }
    invalidatePolling()
    const candidateContextGeneration = contextGeneration
    const candidateActionGeneration = ++actionGeneration
    const controller = createController()
    state.value = 'restoring'
    clearRequestFeedback()
    try {
      const nextRun =
        mode === 'active'
          ? await getActiveAgentRun(candidate.conversationId, controller.signal)
          : await getLatestAgentRun(candidate.conversationId, controller.signal)
      if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
        return { kind: 'stale' }
      }
      availability.value = 'available'
      if (nextRun === null) {
        run.value = null
        state.value = 'idle'
        return { kind: 'empty' }
      }
      if (!applyRun(candidate, candidateContextGeneration, nextRun)) return { kind: 'stale' }
      if (isAgentRunActive(nextRun.status)) {
        startPolling(candidate, candidateContextGeneration, nextRun)
        return { kind: 'active', run: nextRun }
      }
      return { kind: 'terminal', run: nextRun }
    } catch (error) {
      if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
        return { kind: 'stale' }
      }
      const apiError = toApiError(error)
      if (isAgentCapabilityUnavailable(apiError)) {
        markUnavailable()
        return { kind: 'unavailable' }
      }
      state.value = 'idle'
      manualRestoreRequired.value = true
      errorMessage.value = apiError.message
      requestId.value = apiError.requestId
      return { kind: 'failed' }
    } finally {
      releaseController(controller)
    }
  }

  function restoreLatest(): Promise<AgentRestoreOutcome> {
    return restore('latest')
  }

  function restoreActive(): Promise<AgentRestoreOutcome> {
    return restore('active')
  }

  async function queue(params: CreateAgentRunParams): Promise<AgentQueueOutcome> {
    const candidate = context
    if (
      !candidate ||
      retryCooldown.isCoolingDown.value ||
      state.value === 'restoring' ||
      state.value === 'queueing' ||
      state.value === 'canceling' ||
      isActive.value
    ) {
      return { kind: 'failed' }
    }
    if (availability.value === 'unavailable') return { kind: 'unavailable' }
    invalidatePolling()
    const candidateContextGeneration = contextGeneration
    const candidateActionGeneration = ++actionGeneration
    const controller = createController()
    state.value = 'queueing'
    clearRequestFeedback()
    clearCapacityFailure()
    try {
      const result = await createAgentRun(candidate.conversationId, params, controller.signal)
      if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
        return { kind: 'stale' }
      }
      if (!applyRun(candidate, candidateContextGeneration, result.agentRun)) {
        return { kind: 'stale' }
      }
      if (isAgentRunActive(result.agentRun.status)) {
        startPolling(candidate, candidateContextGeneration, result.agentRun)
      }
      return { kind: 'started', result }
    } catch (error) {
      if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
        return { kind: 'stale' }
      }
      const apiError = toApiError(error)
      if (isAgentCapabilityUnavailable(apiError)) {
        markUnavailable()
        return { kind: 'unavailable' }
      }
      const capacity = capacityFailureFromApiError(apiError, 5)
      if (capacity) {
        capacityFailure.value = capacity
        retryCooldown.start(capacity.retryAfterSeconds)
        requestId.value = capacity.requestId
        state.value = 'idle'
        return { kind: 'capacity' }
      }
      if (
        apiError.kind === 'conflict' &&
        (apiError.code === 'conversation_execution_active' ||
          apiError.code === 'conversation_answer_active')
      ) {
        requestId.value = apiError.requestId
        state.value = 'idle'
        return { kind: 'execution-conflict' }
      }
      errorMessage.value = apiError.message
      requestId.value = apiError.requestId
      manualRestoreRequired.value =
        apiError.kind === 'network' || apiError.kind === 'timeout' || apiError.kind === 'server'
      state.value = 'idle'
      return { kind: 'failed' }
    } finally {
      releaseController(controller)
    }
  }

  async function cancel(): Promise<AgentCancelOutcome> {
    const candidate = context
    const currentRun = run.value
    if (!candidate || !currentRun || !currentRun.cancelable) return { kind: 'failed' }
    invalidatePolling()
    const candidateContextGeneration = contextGeneration
    const candidateActionGeneration = ++actionGeneration
    const controller = createController()
    state.value = 'canceling'
    clearRequestFeedback()
    try {
      const canceledRun = await cancelAgentRun(currentRun.id, controller.signal)
      if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
        return { kind: 'stale' }
      }
      if (!applyRun(candidate, candidateContextGeneration, canceledRun)) return { kind: 'stale' }
      await notifyTerminal(candidate, candidateContextGeneration, canceledRun)
      return { kind: 'canceled', run: canceledRun }
    } catch (error) {
      if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
        return { kind: 'stale' }
      }
      const apiError = toApiError(error)
      if (isAgentCapabilityUnavailable(apiError)) {
        markUnavailable()
        return { kind: 'failed' }
      }
      if (
        apiError.kind === 'conflict' &&
        (apiError.code === 'agent_run_processing' || apiError.code === 'agent_run_terminal')
      ) {
        try {
          const refreshedRun = await getAgentRun(currentRun.id, controller.signal)
          if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
            return { kind: 'stale' }
          }
          if (!applyRun(candidate, candidateContextGeneration, refreshedRun)) {
            return { kind: 'stale' }
          }
          if (isAgentRunActive(refreshedRun.status)) {
            startPolling(candidate, candidateContextGeneration, refreshedRun)
          } else {
            await notifyTerminal(candidate, candidateContextGeneration, refreshedRun)
          }
          return { kind: 'refreshed', run: refreshedRun }
        } catch (refreshError) {
          if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
            return { kind: 'stale' }
          }
          const refreshApiError = toApiError(refreshError)
          if (isAgentCapabilityUnavailable(refreshApiError)) {
            markUnavailable()
            return { kind: 'failed' }
          }
          errorMessage.value = refreshApiError.message
          requestId.value = refreshApiError.requestId
          manualRestoreRequired.value = true
          state.value = 'idle'
          return { kind: 'failed' }
        }
      }
      errorMessage.value = apiError.message
      requestId.value = apiError.requestId
      manualRestoreRequired.value = true
      state.value = 'idle'
      return { kind: 'failed' }
    } finally {
      releaseController(controller)
    }
  }

  async function manualRestore(): Promise<AgentRestoreOutcome> {
    const candidate = context
    const currentRun = run.value
    if (!candidate || currentRun === null) return restoreLatest()
    invalidatePolling()
    const candidateContextGeneration = contextGeneration
    const candidateActionGeneration = ++actionGeneration
    const controller = createController()
    state.value = 'restoring'
    clearRequestFeedback()
    try {
      const nextRun = await getAgentRun(currentRun.id, controller.signal)
      if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
        return { kind: 'stale' }
      }
      if (!applyRun(candidate, candidateContextGeneration, nextRun)) return { kind: 'stale' }
      if (isAgentRunActive(nextRun.status)) {
        startPolling(candidate, candidateContextGeneration, nextRun)
        return { kind: 'active', run: nextRun }
      }
      await notifyTerminal(candidate, candidateContextGeneration, nextRun)
      return { kind: 'terminal', run: nextRun }
    } catch (error) {
      if (!actionIsCurrent(candidate, candidateContextGeneration, candidateActionGeneration)) {
        return { kind: 'stale' }
      }
      const apiError = toApiError(error)
      if (isAgentCapabilityUnavailable(apiError)) {
        markUnavailable()
        return { kind: 'unavailable' }
      }
      errorMessage.value = apiError.message
      requestId.value = apiError.requestId
      manualRestoreRequired.value = true
      state.value = 'idle'
      return { kind: 'failed' }
    } finally {
      releaseController(controller)
    }
  }

  function dispose(): void {
    if (disposed) return
    disposed = true
    contextGeneration += 1
    actionGeneration += 1
    invalidatePolling()
    abortControllers()
  }

  onScopeDispose(dispose)

  return {
    availability,
    bindConversation,
    canCancel,
    canSubmit,
    cancel,
    capacityFailure,
    clearCapacityFailure,
    clearConversation,
    clearRequestFeedback,
    errorMessage,
    isActive,
    isBusy,
    isCoolingDown: retryCooldown.isCoolingDown,
    manualRestore,
    manualRestoreRequired,
    queue,
    requestId,
    restoreActive,
    restoreLatest,
    retryAfterSeconds: retryCooldown.remainingSeconds,
    run,
    state,
  }
}
