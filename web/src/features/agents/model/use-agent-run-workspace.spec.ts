import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AgentRun, AgentRunStatus } from '../../../entities/agent/model/agent-run'
import type { ConversationMessage } from '../../../entities/conversation/model/conversation'
import { ApiError } from '../../../shared/api/api-error'
import {
  cancelAgentRun,
  createAgentRun,
  getActiveAgentRun,
  getAgentRun,
  getLatestAgentRun,
} from '../api/agent-api'
import { useAgentRunWorkspace } from './use-agent-run-workspace'

vi.mock('../api/agent-api', async () => {
  const actual = await vi.importActual<typeof import('../api/agent-api')>('../api/agent-api')
  return {
    ...actual,
    cancelAgentRun: vi.fn(),
    createAgentRun: vi.fn(),
    getActiveAgentRun: vi.fn(),
    getAgentRun: vi.fn(),
    getLatestAgentRun: vi.fn(),
  }
})

const cancelAgentRunMock = vi.mocked(cancelAgentRun)
const createAgentRunMock = vi.mocked(createAgentRun)
const getActiveAgentRunMock = vi.mocked(getActiveAgentRun)
const getAgentRunMock = vi.mocked(getAgentRun)
const getLatestAgentRunMock = vi.mocked(getLatestAgentRun)

const userMessage: ConversationMessage = {
  id: 201,
  conversationId: 11,
  sequenceNo: 1,
  role: 'user',
  content: '比较两份文档',
  status: 'completed',
  clientMessageId: null,
  createdAt: new Date('2026-11-16T10:00:00Z'),
  updatedAt: new Date('2026-11-16T10:00:00Z'),
}

const assistantMessage: ConversationMessage = {
  ...userMessage,
  id: 202,
  sequenceNo: 2,
  role: 'assistant',
  content: '正在生成回答…',
  status: 'generating',
}

function createRun(status: AgentRunStatus, id = 701): AgentRun {
  const terminal = status === 'succeeded' || status === 'failed' || status === 'canceled'
  const succeededSteps: AgentRun['steps'] =
    status === 'succeeded'
      ? [
          {
            id: id * 10 + 1,
            agentRunId: id,
            sequenceNo: 1,
            kind: 'tool_call',
            status: 'succeeded',
            toolName: 'search_documents',
            error: null,
            tokenUsage: { promptTokens: 4, completionTokens: 1, totalTokens: 5 },
            createdAt: new Date('2026-11-16T10:00:00Z'),
            startedAt: new Date('2026-11-16T10:00:00Z'),
            completedAt: new Date('2026-11-16T10:00:00.200Z'),
          },
          {
            id: id * 10 + 2,
            agentRunId: id,
            sequenceNo: 2,
            kind: 'tool_call',
            status: 'succeeded',
            toolName: 'read_document_evidence',
            error: null,
            tokenUsage: { promptTokens: 3, completionTokens: 1, totalTokens: 4 },
            createdAt: new Date('2026-11-16T10:00:00.300Z'),
            startedAt: new Date('2026-11-16T10:00:00.300Z'),
            completedAt: new Date('2026-11-16T10:00:00.500Z'),
          },
          {
            id: id * 10 + 3,
            agentRunId: id,
            sequenceNo: 3,
            kind: 'final',
            status: 'succeeded',
            toolName: null,
            error: null,
            tokenUsage: { promptTokens: 3, completionTokens: 3, totalTokens: 6 },
            createdAt: new Date('2026-11-16T10:00:00.600Z'),
            startedAt: new Date('2026-11-16T10:00:00.600Z'),
            completedAt: new Date('2026-11-16T10:00:01Z'),
          },
        ]
      : []
  const failedSteps: AgentRun['steps'] =
    status === 'failed'
      ? [
          {
            id: id * 10 + 1,
            agentRunId: id,
            sequenceNo: 1,
            kind: 'tool_call',
            status: 'failed',
            toolName: 'search_documents',
            error: { code: 'agent_tool_failed', message: 'Agent tool execution failed' },
            tokenUsage: { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
            createdAt: new Date('2026-11-16T10:00:00Z'),
            startedAt: new Date('2026-11-16T10:00:00Z'),
            completedAt: new Date('2026-11-16T10:00:01Z'),
          },
        ]
      : []
  const steps = status === 'succeeded' ? succeededSteps : failedSteps
  return {
    id,
    conversationId: 11,
    userMessageId: 201,
    assistantMessageId: 202,
    clientRunId: `run-${id}`,
    requestedDocumentId: null,
    requestedResponseLanguage: 'auto',
    status,
    stepCount: steps.length,
    maxSteps: 5,
    maxTotalTokens: 48_000,
    cancelable: status === 'queued',
    steps,
    result:
      status === 'succeeded'
        ? {
            answer: '已完成回答。[1]',
            responseLanguage: 'zh',
            sources: [
              {
                citation: 1,
                evidenceRef: 'step-1:item-1',
                documentId: 123,
                chunkId: 456,
                title: '系统设计',
                pageStart: 1,
                pageEnd: 1,
                contentHash: 'a'.repeat(64),
              },
            ],
          }
        : null,
    error:
      status === 'failed'
        ? { code: 'agent_execution_failed', message: 'Agent execution failed' }
        : null,
    tokenUsage:
      status === 'succeeded'
        ? { promptTokens: 10, completionTokens: 5, totalTokens: 15 }
        : { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
    createdAt: new Date('2026-11-16T10:00:00Z'),
    updatedAt: new Date('2026-11-16T10:00:01Z'),
    startedAt:
      status === 'queued' || status === 'canceled' ? null : new Date('2026-11-16T10:00:00Z'),
    completedAt: terminal ? new Date('2026-11-16T10:00:01Z') : null,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolver, rejecter) => {
    resolve = resolver
    reject = rejecter
  })
  return { promise, resolve, reject }
}

beforeEach(() => {
  cancelAgentRunMock.mockReset()
  createAgentRunMock.mockReset()
  getActiveAgentRunMock.mockReset()
  getAgentRunMock.mockReset()
  getLatestAgentRunMock.mockReset()
})

afterEach(() => vi.useRealTimers())

describe('useAgentRunWorkspace', () => {
  it('restores an active Run, polls growth to terminal, and notifies once', async () => {
    vi.useFakeTimers()
    const onTerminal = vi.fn()
    getLatestAgentRunMock.mockResolvedValue(createRun('queued'))
    getAgentRunMock
      .mockResolvedValueOnce(createRun('processing'))
      .mockResolvedValueOnce(createRun('succeeded'))

    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace({ pollIntervalMs: 20, onTerminal }))!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })

    await expect(workspace.restoreLatest()).resolves.toMatchObject({ kind: 'active' })
    await vi.waitFor(() => expect(workspace.run.value?.status).toBe('processing'))
    await vi.advanceTimersByTimeAsync(20)
    await vi.waitFor(() => expect(workspace.run.value?.status).toBe('succeeded'))

    expect(workspace.state.value).toBe('terminal')
    expect(onTerminal).toHaveBeenCalledTimes(1)
    scope.stop()
  })

  it('stops after a polling failure and supports explicit manual recovery', async () => {
    vi.useFakeTimers()
    getLatestAgentRunMock.mockResolvedValue(createRun('queued'))
    getAgentRunMock
      .mockRejectedValueOnce(new ApiError('network', 'poll failed'))
      .mockResolvedValueOnce(createRun('succeeded'))

    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace({ pollIntervalMs: 20 }))!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await workspace.restoreLatest()
    await vi.waitFor(() => expect(workspace.manualRestoreRequired.value).toBe(true))

    await vi.advanceTimersByTimeAsync(100)
    expect(getAgentRunMock).toHaveBeenCalledTimes(1)
    await expect(workspace.manualRestore()).resolves.toMatchObject({ kind: 'terminal' })
    expect(workspace.run.value?.status).toBe('succeeded')
    expect(workspace.manualRestoreRequired.value).toBe(false)
    scope.stop()
  })

  it('retries terminal message refresh when the first callback fails', async () => {
    const onTerminal = vi.fn().mockRejectedValueOnce(new Error('message refresh failed'))
    getLatestAgentRunMock.mockResolvedValue(createRun('queued'))
    getAgentRunMock
      .mockResolvedValueOnce(createRun('succeeded'))
      .mockResolvedValueOnce(createRun('succeeded'))
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace({ onTerminal }))!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await workspace.restoreLatest()
    await vi.waitFor(() => expect(workspace.manualRestoreRequired.value).toBe(true))
    expect(workspace.errorMessage.value).toContain('最终消息刷新失败')

    await expect(workspace.manualRestore()).resolves.toMatchObject({ kind: 'terminal' })
    expect(onTerminal).toHaveBeenCalledTimes(2)
    expect(workspace.manualRestoreRequired.value).toBe(false)
    scope.stop()
  })

  it('clears a stale active Run when the capability disappears during polling', async () => {
    getLatestAgentRunMock.mockResolvedValue(createRun('queued'))
    getAgentRunMock.mockRejectedValueOnce(
      new ApiError('not-found', 'route missing', { status: 404 }),
    )
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await workspace.restoreLatest()
    await vi.waitFor(() => expect(workspace.availability.value).toBe('unavailable'))

    expect(workspace.run.value).toBeNull()
    expect(workspace.isActive.value).toBe(false)
    expect(workspace.manualRestoreRequired.value).toBe(false)
    scope.stop()
  })

  it('rejects queue while a restore action is unresolved', async () => {
    const lateLatest = deferred<AgentRun | null>()
    getLatestAgentRunMock.mockReturnValue(lateLatest.promise)
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    const restorePromise = workspace.restoreLatest()

    await expect(
      workspace.queue({
        content: userMessage.content,
        clientRunId: 'run-701',
        responseLanguage: 'auto',
      }),
    ).resolves.toEqual({ kind: 'failed' })
    expect(createAgentRunMock).not.toHaveBeenCalled()
    lateLatest.resolve(null)
    await restorePromise
    scope.stop()
  })

  it('distinguishes a disabled route from stable not-found errors', async () => {
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    getLatestAgentRunMock.mockRejectedValueOnce(
      new ApiError('not-found', 'route missing', { status: 404 }),
    )
    await expect(workspace.restoreLatest()).resolves.toEqual({ kind: 'unavailable' })
    expect(workspace.availability.value).toBe('unavailable')

    workspace.bindConversation({ conversationId: 11, selectionGeneration: 2 })
    getLatestAgentRunMock.mockRejectedValueOnce(
      new ApiError('not-found', 'conversation missing', {
        status: 404,
        code: 'conversation_not_found',
      }),
    )
    await expect(workspace.restoreLatest()).resolves.toEqual({ kind: 'failed' })
    expect(workspace.errorMessage.value).toBe('conversation missing')
    scope.stop()
  })

  it('queues a Run, returns cross-execution conflicts, and never auto replays', async () => {
    const pendingPoll = deferred<AgentRun>()
    getAgentRunMock.mockReturnValue(pendingPoll.promise)
    createAgentRunMock
      .mockResolvedValueOnce({
        conversationId: 11,
        userMessage,
        assistantMessage,
        agentRun: createRun('queued'),
      })
      .mockRejectedValueOnce(
        new ApiError('conflict', 'busy', {
          status: 409,
          code: 'conversation_execution_active',
        }),
      )
      .mockRejectedValueOnce(
        new ApiError('conflict', 'busy', {
          status: 409,
          code: 'conversation_answer_active',
        }),
      )

    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await expect(
      workspace.queue({
        content: userMessage.content,
        clientRunId: 'run-701',
        responseLanguage: 'auto',
      }),
    ).resolves.toMatchObject({ kind: 'started' })
    expect(workspace.run.value?.status).toBe('queued')

    workspace.bindConversation({ conversationId: 11, selectionGeneration: 2 })
    await expect(
      workspace.queue({
        content: userMessage.content,
        clientRunId: 'run-702',
        responseLanguage: 'auto',
      }),
    ).resolves.toEqual({ kind: 'execution-conflict' })
    await expect(
      workspace.queue({
        content: userMessage.content,
        clientRunId: 'run-703',
        responseLanguage: 'auto',
      }),
    ).resolves.toEqual({ kind: 'execution-conflict' })
    expect(createAgentRunMock).toHaveBeenCalledTimes(3)
    scope.stop()
  })

  it('applies Agent capacity cooldown and does not replay when it expires', async () => {
    vi.useFakeTimers()
    createAgentRunMock.mockRejectedValueOnce(
      new ApiError('rate-limited', 'owner capacity', {
        status: 429,
        code: 'agent_run_owner_queue_capacity',
        requestId: 'agent-capacity-1',
        retryAfterSeconds: 5,
      }),
    )
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })

    await expect(
      workspace.queue({
        content: userMessage.content,
        clientRunId: 'run-701',
        responseLanguage: 'auto',
      }),
    ).resolves.toEqual({ kind: 'capacity' })
    expect(workspace.capacityFailure.value?.code).toBe('agent_run_owner_queue_capacity')
    expect(workspace.retryAfterSeconds.value).toBe(5)

    await vi.advanceTimersByTimeAsync(5_000)
    expect(createAgentRunMock).toHaveBeenCalledTimes(1)
    scope.stop()
  })

  it('applies global 503 capacity cooldown without treating a generic server error as capacity', async () => {
    vi.useFakeTimers()
    createAgentRunMock.mockRejectedValueOnce(
      new ApiError('server', 'global capacity', {
        status: 503,
        code: 'agent_run_queue_capacity',
        requestId: 'agent-global-1',
        retryAfterSeconds: 3,
      }),
    )
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await expect(
      workspace.queue({
        content: userMessage.content,
        clientRunId: 'run-701',
        responseLanguage: 'auto',
      }),
    ).resolves.toEqual({ kind: 'capacity' })
    expect(workspace.capacityFailure.value).toMatchObject({
      code: 'agent_run_queue_capacity',
      requestId: 'agent-global-1',
      retryAfterSeconds: 3,
    })
    scope.stop()
  })

  it('fences late create and poll responses after switching conversation', async () => {
    const lateCreate = deferred<Awaited<ReturnType<typeof createAgentRun>>>()
    const latePoll = deferred<AgentRun>()
    createAgentRunMock.mockReturnValue(lateCreate.promise)
    getLatestAgentRunMock.mockResolvedValue(createRun('queued'))
    getAgentRunMock.mockReturnValue(latePoll.promise)

    let selectedConversationId = 11
    const scope = effectScope()
    const workspace = scope.run(() =>
      useAgentRunWorkspace({
        isSelectionCurrent: (context) => context.conversationId === selectedConversationId,
      }),
    )!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    const createPromise = workspace.queue({
      content: userMessage.content,
      clientRunId: 'run-701',
      responseLanguage: 'auto',
    })
    selectedConversationId = 12
    workspace.bindConversation({ conversationId: 12, selectionGeneration: 2 })
    lateCreate.resolve({
      conversationId: 11,
      userMessage,
      assistantMessage,
      agentRun: createRun('queued'),
    })
    await expect(createPromise).resolves.toEqual({ kind: 'stale' })
    expect(workspace.run.value).toBeNull()

    selectedConversationId = 11
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 3 })
    await workspace.restoreLatest()
    selectedConversationId = 12
    workspace.bindConversation({ conversationId: 12, selectionGeneration: 4 })
    latePoll.resolve(createRun('processing'))
    await Promise.resolve()
    expect(workspace.run.value).toBeNull()
    scope.stop()
  })

  it('fences late latest and active restores after switching conversation', async () => {
    const lateLatest = deferred<AgentRun | null>()
    const lateActive = deferred<AgentRun | null>()
    getLatestAgentRunMock.mockReturnValueOnce(lateLatest.promise)
    getActiveAgentRunMock.mockReturnValueOnce(lateActive.promise)
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!

    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    const latestPromise = workspace.restoreLatest()
    workspace.bindConversation({ conversationId: 12, selectionGeneration: 2 })
    lateLatest.resolve(createRun('queued'))
    await expect(latestPromise).resolves.toEqual({ kind: 'stale' })
    expect(workspace.run.value).toBeNull()

    workspace.bindConversation({ conversationId: 11, selectionGeneration: 3 })
    const activePromise = workspace.restoreActive()
    workspace.bindConversation({ conversationId: 12, selectionGeneration: 4 })
    lateActive.resolve(createRun('processing'))
    await expect(activePromise).resolves.toEqual({ kind: 'stale' })
    expect(workspace.run.value).toBeNull()
    scope.stop()
  })

  it('invalidates an in-flight poll before cancel and refreshes conflicts from the server', async () => {
    const latePoll = deferred<AgentRun>()
    getLatestAgentRunMock.mockResolvedValue(createRun('queued'))
    getAgentRunMock.mockReturnValueOnce(latePoll.promise)
    cancelAgentRunMock.mockResolvedValueOnce(createRun('canceled'))

    const onTerminal = vi.fn()
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace({ onTerminal }))!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await workspace.restoreLatest()
    await expect(workspace.cancel()).resolves.toMatchObject({ kind: 'canceled' })
    latePoll.resolve(createRun('processing'))
    await Promise.resolve()
    expect(workspace.run.value?.status).toBe('canceled')
    expect(onTerminal).toHaveBeenCalledTimes(1)

    workspace.bindConversation({ conversationId: 11, selectionGeneration: 2 })
    getLatestAgentRunMock.mockResolvedValueOnce(createRun('queued'))
    getAgentRunMock
      .mockResolvedValueOnce(createRun('queued'))
      .mockResolvedValueOnce(createRun('processing'))
    cancelAgentRunMock.mockRejectedValueOnce(
      new ApiError('conflict', 'processing', { status: 409, code: 'agent_run_processing' }),
    )
    await workspace.restoreLatest()
    await expect(workspace.cancel()).resolves.toMatchObject({
      kind: 'refreshed',
      run: { status: 'processing' },
    })

    workspace.bindConversation({ conversationId: 11, selectionGeneration: 3 })
    getLatestAgentRunMock.mockResolvedValueOnce(createRun('queued'))
    getAgentRunMock
      .mockResolvedValueOnce(createRun('queued'))
      .mockResolvedValueOnce(createRun('succeeded'))
    cancelAgentRunMock.mockRejectedValueOnce(
      new ApiError('conflict', 'terminal', { status: 409, code: 'agent_run_terminal' }),
    )
    await workspace.restoreLatest()
    await expect(workspace.cancel()).resolves.toMatchObject({
      kind: 'refreshed',
      run: { status: 'succeeded' },
    })
    scope.stop()
  })

  it('clears an active Run when cancel discovers a disabled route', async () => {
    const latePoll = deferred<AgentRun>()
    getLatestAgentRunMock.mockResolvedValue(createRun('queued'))
    getAgentRunMock.mockReturnValue(latePoll.promise)
    cancelAgentRunMock.mockRejectedValueOnce(
      new ApiError('not-found', 'route missing', { status: 404 }),
    )
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await workspace.restoreLatest()
    await expect(workspace.cancel()).resolves.toEqual({ kind: 'failed' })
    expect(workspace.availability.value).toBe('unavailable')
    expect(workspace.run.value).toBeNull()
    scope.stop()
  })

  it('fences a late cancel after switching conversation', async () => {
    const latePoll = deferred<AgentRun>()
    const lateCancel = deferred<AgentRun>()
    getLatestAgentRunMock.mockResolvedValue(createRun('queued'))
    getAgentRunMock.mockReturnValue(latePoll.promise)
    cancelAgentRunMock.mockReturnValue(lateCancel.promise)
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace())!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await workspace.restoreLatest()
    const cancelPromise = workspace.cancel()
    workspace.bindConversation({ conversationId: 12, selectionGeneration: 2 })
    lateCancel.resolve(createRun('canceled'))
    await expect(cancelPromise).resolves.toEqual({ kind: 'stale' })
    expect(workspace.run.value).toBeNull()
    scope.stop()
  })

  it('ignores in-flight action results after scope disposal', async () => {
    const lateCreate = deferred<Awaited<ReturnType<typeof createAgentRun>>>()
    const onTerminal = vi.fn()
    createAgentRunMock.mockReturnValue(lateCreate.promise)
    const scope = effectScope()
    const workspace = scope.run(() => useAgentRunWorkspace({ onTerminal }))!
    workspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    const queuePromise = workspace.queue({
      content: userMessage.content,
      clientRunId: 'run-701',
      responseLanguage: 'auto',
    })
    scope.stop()
    lateCreate.resolve({
      conversationId: 11,
      userMessage,
      assistantMessage,
      agentRun: createRun('succeeded'),
    })
    await expect(queuePromise).resolves.toEqual({ kind: 'stale' })
    expect(workspace.run.value).toBeNull()
    expect(onTerminal).not.toHaveBeenCalled()
  })

  it('treats generic 404 from manual and cancel-conflict refresh as unavailable', async () => {
    getLatestAgentRunMock.mockResolvedValueOnce(createRun('succeeded'))
    getAgentRunMock.mockRejectedValueOnce(
      new ApiError('not-found', 'route missing', { status: 404 }),
    )
    const firstScope = effectScope()
    const firstWorkspace = firstScope.run(() => useAgentRunWorkspace())!
    firstWorkspace.bindConversation({ conversationId: 11, selectionGeneration: 1 })
    await firstWorkspace.restoreLatest()
    await expect(firstWorkspace.manualRestore()).resolves.toEqual({ kind: 'unavailable' })
    expect(firstWorkspace.run.value).toBeNull()
    firstScope.stop()

    const latePoll = deferred<AgentRun>()
    getLatestAgentRunMock.mockResolvedValueOnce(createRun('queued'))
    getAgentRunMock
      .mockReturnValueOnce(latePoll.promise)
      .mockRejectedValueOnce(new ApiError('not-found', 'route missing', { status: 404 }))
    cancelAgentRunMock.mockRejectedValueOnce(
      new ApiError('conflict', 'processing', { status: 409, code: 'agent_run_processing' }),
    )
    const secondScope = effectScope()
    const secondWorkspace = secondScope.run(() => useAgentRunWorkspace())!
    secondWorkspace.bindConversation({ conversationId: 11, selectionGeneration: 2 })
    await secondWorkspace.restoreLatest()
    await expect(secondWorkspace.cancel()).resolves.toEqual({ kind: 'failed' })
    expect(secondWorkspace.availability.value).toBe('unavailable')
    expect(secondWorkspace.run.value).toBeNull()
    secondScope.stop()
  })
})
