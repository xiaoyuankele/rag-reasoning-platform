import type {
  AgentExecutionError,
  AgentExecutionErrorCode,
  AgentResponseLanguage,
  AgentRun,
  AgentRunResult,
  AgentRunStatus,
  AgentSource,
  AgentStep,
  AgentStepKind,
  AgentStepStatus,
  AgentTokenUsage,
  AgentToolName,
} from '../../../entities/agent/model/agent-run'
import type { ConversationMessage } from '../../../entities/conversation/model/conversation'
import { ApiError, toApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

const runStatuses = new Set<AgentRunStatus>([
  'queued',
  'processing',
  'succeeded',
  'failed',
  'canceled',
])
const stepKinds = new Set<AgentStepKind>(['tool_call', 'final'])
const stepStatuses = new Set<AgentStepStatus>(['pending', 'running', 'succeeded', 'failed'])
const toolNames = new Set<AgentToolName>(['search_documents', 'read_document_evidence'])
const executionErrorMessages: Record<AgentExecutionErrorCode, string> = {
  agent_execution_failed: 'Agent execution failed',
  agent_temporarily_unavailable: 'Agent is temporarily unavailable',
  agent_step_limit_exceeded: 'Agent step limit exceeded',
  agent_token_budget_exceeded: 'Agent token budget exceeded',
  agent_deadline_exceeded: 'Agent execution deadline exceeded',
  agent_invalid_tool_call: 'Agent produced an invalid tool call',
  agent_tool_failed: 'Agent tool execution failed',
  agent_worker_interrupted: 'Agent worker was interrupted',
}
const executionErrorCodes = new Set<AgentExecutionErrorCode>(
  Object.keys(executionErrorMessages) as AgentExecutionErrorCode[],
)

const messageKeys = [
  'id',
  'conversation_id',
  'sequence_no',
  'role',
  'content',
  'status',
  'client_message_id',
  'created_at',
  'updated_at',
] as const
const tokenUsageKeys = ['prompt_tokens', 'completion_tokens', 'total_tokens'] as const
const errorKeys = ['code', 'message'] as const
const sourceKeys = [
  'citation',
  'evidence_ref',
  'document_id',
  'chunk_id',
  'title',
  'page_start',
  'page_end',
  'content_hash',
] as const
const resultKeys = ['answer', 'response_language', 'sources'] as const
const stepKeys = [
  'id',
  'agent_run_id',
  'sequence_no',
  'kind',
  'status',
  'tool_name',
  'error',
  'token_usage',
  'created_at',
  'started_at',
  'completed_at',
] as const
const runKeys = [
  'id',
  'conversation_id',
  'user_message_id',
  'assistant_message_id',
  'client_run_id',
  'requested_document_id',
  'requested_response_language',
  'status',
  'step_count',
  'max_steps',
  'max_total_tokens',
  'cancelable',
  'steps',
  'result',
  'error',
  'token_usage',
  'created_at',
  'updated_at',
  'started_at',
  'completed_at',
] as const

interface CreateAgentRunParams {
  content: string
  clientRunId: string
  documentId?: number
  responseLanguage: AgentResponseLanguage
}

export interface AgentRunStartResult {
  conversationId: number
  userMessage: ConversationMessage
  assistantMessage: ConversationMessage
  agentRun: AgentRun
}

function invalidResponse(message = '后端 Agent 响应不符合约定。'): never {
  throw new ApiError('invalid-response', message)
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function hasExactKeys<const TKeys extends readonly string[]>(
  value: unknown,
  keys: TKeys,
): value is Record<TKeys[number], unknown> {
  if (!isRecord(value)) return false
  const actual = Object.keys(value)
  return actual.length === keys.length && keys.every((key) => Object.hasOwn(value, key))
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function isNonNegativeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isDateTime(value: unknown): value is string {
  return typeof value === 'string' && !Number.isNaN(Date.parse(value))
}

function isNullableDateTime(value: unknown): value is string | null {
  return value === null || isDateTime(value)
}

function mapTokenUsage(value: unknown): AgentTokenUsage {
  if (
    !hasExactKeys(value, tokenUsageKeys) ||
    !isNonNegativeInteger(value.prompt_tokens) ||
    !isNonNegativeInteger(value.completion_tokens) ||
    !isNonNegativeInteger(value.total_tokens) ||
    value.prompt_tokens + value.completion_tokens !== value.total_tokens
  ) {
    invalidResponse()
  }
  return {
    promptTokens: value.prompt_tokens,
    completionTokens: value.completion_tokens,
    totalTokens: value.total_tokens,
  }
}

function mapExecutionError(value: unknown): AgentExecutionError {
  if (
    !hasExactKeys(value, errorKeys) ||
    typeof value.code !== 'string' ||
    !executionErrorCodes.has(value.code as AgentExecutionErrorCode) ||
    typeof value.message !== 'string' ||
    value.message !== executionErrorMessages[value.code as AgentExecutionErrorCode]
  ) {
    invalidResponse()
  }
  return {
    code: value.code as AgentExecutionErrorCode,
    message: executionErrorMessages[value.code as AgentExecutionErrorCode],
  }
}

function mapConversationMessage(value: unknown): ConversationMessage {
  if (
    !hasExactKeys(value, messageKeys) ||
    !isPositiveInteger(value.id) ||
    !isPositiveInteger(value.conversation_id) ||
    !isPositiveInteger(value.sequence_no) ||
    (value.role !== 'user' && value.role !== 'assistant') ||
    typeof value.content !== 'string' ||
    value.content.trim().length === 0 ||
    (value.status !== 'completed' &&
      value.status !== 'generating' &&
      value.status !== 'failed' &&
      value.status !== 'interrupted') ||
    (value.client_message_id !== null && typeof value.client_message_id !== 'string') ||
    !isDateTime(value.created_at) ||
    !isDateTime(value.updated_at)
  ) {
    invalidResponse('后端 Agent 创建消息不符合约定。')
  }
  return {
    id: value.id,
    conversationId: value.conversation_id,
    sequenceNo: value.sequence_no,
    role: value.role,
    content: value.content,
    status: value.status,
    clientMessageId: value.client_message_id,
    createdAt: new Date(value.created_at),
    updatedAt: new Date(value.updated_at),
  }
}

function mapStep(value: unknown, runId: number, expectedSequence: number): AgentStep {
  if (
    !hasExactKeys(value, stepKeys) ||
    !isPositiveInteger(value.id) ||
    value.agent_run_id !== runId ||
    value.sequence_no !== expectedSequence ||
    typeof value.kind !== 'string' ||
    !stepKinds.has(value.kind as AgentStepKind) ||
    typeof value.status !== 'string' ||
    !stepStatuses.has(value.status as AgentStepStatus) ||
    !isDateTime(value.created_at) ||
    !isNullableDateTime(value.started_at) ||
    !isNullableDateTime(value.completed_at)
  ) {
    invalidResponse()
  }

  const kind = value.kind as AgentStepKind
  const status = value.status as AgentStepStatus
  const toolName = value.tool_name
  if (
    (kind === 'tool_call' &&
      (typeof toolName !== 'string' || !toolNames.has(toolName as AgentToolName))) ||
    (kind === 'final' && (toolName !== null || status !== 'succeeded'))
  ) {
    invalidResponse()
  }
  if (
    (status === 'pending' && (value.started_at !== null || value.completed_at !== null)) ||
    (status === 'running' && (value.started_at === null || value.completed_at !== null)) ||
    ((status === 'succeeded' || status === 'failed') &&
      (value.started_at === null || value.completed_at === null))
  ) {
    invalidResponse()
  }

  const error = value.error === null ? null : mapExecutionError(value.error)
  if ((status === 'failed') !== (error !== null)) invalidResponse()
  const tokenUsage = mapTokenUsage(value.token_usage)
  const createdAt = new Date(value.created_at)
  const startedAt = value.started_at === null ? null : new Date(value.started_at)
  const completedAt = value.completed_at === null ? null : new Date(value.completed_at)
  if (
    (startedAt !== null && startedAt.getTime() < createdAt.getTime()) ||
    (completedAt !== null && completedAt.getTime() < (startedAt?.getTime() ?? createdAt.getTime()))
  ) {
    invalidResponse()
  }

  return {
    id: value.id,
    agentRunId: runId,
    sequenceNo: expectedSequence,
    kind,
    status,
    toolName: toolName as AgentToolName | null,
    error,
    tokenUsage,
    createdAt,
    startedAt,
    completedAt,
  }
}

function mapResult(value: unknown, steps: AgentStep[]): AgentRunResult {
  if (
    !hasExactKeys(value, resultKeys) ||
    typeof value.answer !== 'string' ||
    value.answer.trim().length === 0 ||
    (value.response_language !== 'zh' && value.response_language !== 'en') ||
    !Array.isArray(value.sources) ||
    value.sources.length < 1 ||
    value.sources.length > 10
  ) {
    invalidResponse()
  }

  const seenEvidenceRefs = new Set<string>()
  const sources: AgentSource[] = value.sources.map((source, index) => {
    if (
      !hasExactKeys(source, sourceKeys) ||
      source.citation !== index + 1 ||
      typeof source.evidence_ref !== 'string' ||
      !/^step-[1-9]\d*:item-[1-9]\d*$/.test(source.evidence_ref) ||
      seenEvidenceRefs.has(source.evidence_ref) ||
      !isPositiveInteger(source.document_id) ||
      !isPositiveInteger(source.chunk_id) ||
      typeof source.title !== 'string' ||
      typeof source.content_hash !== 'string' ||
      !/^[0-9a-f]{64}$/.test(source.content_hash)
    ) {
      invalidResponse()
    }
    const pageRangeIsValid =
      (source.page_start === null && source.page_end === null) ||
      (isPositiveInteger(source.page_start) &&
        isPositiveInteger(source.page_end) &&
        source.page_start <= source.page_end)
    if (!pageRangeIsValid) invalidResponse()

    const sequence = Number(/^step-([1-9]\d*):/.exec(source.evidence_ref)?.[1])
    const evidenceStep = steps[sequence - 1]
    if (
      !evidenceStep ||
      evidenceStep.sequenceNo !== sequence ||
      evidenceStep.kind !== 'tool_call' ||
      evidenceStep.toolName !== 'search_documents' ||
      evidenceStep.status !== 'succeeded'
    ) {
      invalidResponse()
    }
    seenEvidenceRefs.add(source.evidence_ref)
    return {
      citation: source.citation as number,
      evidenceRef: source.evidence_ref,
      documentId: source.document_id,
      chunkId: source.chunk_id,
      title: source.title,
      pageStart: source.page_start as number | null,
      pageEnd: source.page_end as number | null,
      contentHash: source.content_hash as string,
    }
  })
  return { answer: value.answer, responseLanguage: value.response_language, sources }
}

export function mapAgentRunDto(value: unknown): AgentRun {
  if (
    !hasExactKeys(value, runKeys) ||
    !isPositiveInteger(value.id) ||
    !isPositiveInteger(value.conversation_id) ||
    !isPositiveInteger(value.user_message_id) ||
    !isPositiveInteger(value.assistant_message_id) ||
    value.user_message_id === value.assistant_message_id ||
    typeof value.client_run_id !== 'string' ||
    value.client_run_id.trim() !== value.client_run_id ||
    [...value.client_run_id].length < 1 ||
    [...value.client_run_id].length > 128 ||
    (value.requested_document_id !== null && !isPositiveInteger(value.requested_document_id)) ||
    (value.requested_response_language !== 'auto' &&
      value.requested_response_language !== 'zh' &&
      value.requested_response_language !== 'en') ||
    typeof value.status !== 'string' ||
    !runStatuses.has(value.status as AgentRunStatus) ||
    !isNonNegativeInteger(value.step_count) ||
    !isPositiveInteger(value.max_steps) ||
    value.step_count > value.max_steps ||
    !isPositiveInteger(value.max_total_tokens) ||
    typeof value.cancelable !== 'boolean' ||
    !Array.isArray(value.steps) ||
    value.steps.length !== value.step_count ||
    !isDateTime(value.created_at) ||
    !isDateTime(value.updated_at) ||
    !isNullableDateTime(value.started_at) ||
    !isNullableDateTime(value.completed_at)
  ) {
    invalidResponse()
  }

  const status = value.status as AgentRunStatus
  if (value.cancelable !== (status === 'queued')) invalidResponse()
  const steps = value.steps.map((step, index) => mapStep(step, value.id as number, index + 1))
  if (new Set(steps.map((step) => step.id)).size !== steps.length) invalidResponse()
  const error = value.error === null ? null : mapExecutionError(value.error)
  if ((status === 'failed') !== (error !== null)) invalidResponse()
  const result = value.result === null ? null : mapResult(value.result, steps)
  if ((status === 'succeeded') !== (result !== null)) invalidResponse()

  const active = status === 'queued' || status === 'processing'
  const terminal = !active
  if ((active && value.completed_at !== null) || (terminal && value.completed_at === null)) {
    invalidResponse()
  }
  if (
    (status === 'processing' || status === 'succeeded' || status === 'failed') &&
    value.started_at === null
  ) {
    invalidResponse()
  }
  if (terminal && steps.some((step) => step.status === 'pending' || step.status === 'running')) {
    invalidResponse()
  }
  const finalSteps = steps.filter((step) => step.kind === 'final')
  if (
    (status === 'succeeded' &&
      (finalSteps.length !== 1 || finalSteps[0] !== steps[steps.length - 1])) ||
    (status !== 'succeeded' && finalSteps.length !== 0) ||
    (status === 'succeeded' && steps.some((step) => step.status === 'failed'))
  ) {
    invalidResponse()
  }

  const tokenUsage = mapTokenUsage(value.token_usage)
  const summedUsage = steps.reduce(
    (sum, step) => ({
      promptTokens: sum.promptTokens + step.tokenUsage.promptTokens,
      completionTokens: sum.completionTokens + step.tokenUsage.completionTokens,
      totalTokens: sum.totalTokens + step.tokenUsage.totalTokens,
    }),
    { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
  )
  if (
    !Number.isSafeInteger(summedUsage.promptTokens) ||
    !Number.isSafeInteger(summedUsage.completionTokens) ||
    !Number.isSafeInteger(summedUsage.totalTokens) ||
    tokenUsage.promptTokens !== summedUsage.promptTokens ||
    tokenUsage.completionTokens !== summedUsage.completionTokens ||
    tokenUsage.totalTokens !== summedUsage.totalTokens ||
    tokenUsage.totalTokens > value.max_total_tokens
  ) {
    invalidResponse()
  }

  const createdAt = new Date(value.created_at)
  const updatedAt = new Date(value.updated_at)
  const startedAt = value.started_at === null ? null : new Date(value.started_at)
  const completedAt = value.completed_at === null ? null : new Date(value.completed_at)
  if (
    updatedAt.getTime() < createdAt.getTime() ||
    (startedAt !== null && startedAt.getTime() < createdAt.getTime()) ||
    (completedAt !== null &&
      completedAt.getTime() < (startedAt?.getTime() ?? createdAt.getTime())) ||
    (startedAt !== null && updatedAt.getTime() < startedAt.getTime()) ||
    (completedAt !== null && updatedAt.getTime() < completedAt.getTime())
  ) {
    invalidResponse()
  }

  return {
    id: value.id,
    conversationId: value.conversation_id,
    userMessageId: value.user_message_id,
    assistantMessageId: value.assistant_message_id,
    clientRunId: value.client_run_id,
    requestedDocumentId: value.requested_document_id as number | null,
    requestedResponseLanguage: value.requested_response_language,
    status,
    stepCount: value.step_count,
    maxSteps: value.max_steps,
    maxTotalTokens: value.max_total_tokens,
    cancelable: value.cancelable,
    steps,
    result,
    error,
    tokenUsage,
    createdAt,
    updatedAt,
    startedAt,
    completedAt,
  }
}

function mapAgentRunEnvelope(
  value: unknown,
  expected: { runId?: number; conversationId?: number; active?: boolean } = {},
): AgentRun {
  if (!hasExactKeys(value, ['agent_run'] as const)) invalidResponse()
  const run = mapAgentRunDto(value.agent_run)
  if (
    (expected.runId !== undefined && run.id !== expected.runId) ||
    (expected.conversationId !== undefined && run.conversationId !== expected.conversationId) ||
    (expected.active === true && run.status !== 'queued' && run.status !== 'processing')
  ) {
    invalidResponse()
  }
  return run
}

export function mapCreateAgentRunResponse(
  value: unknown,
  conversationId: number,
  params: CreateAgentRunParams,
): AgentRunStartResult {
  if (
    !hasExactKeys(value, [
      'conversation_id',
      'user_message',
      'assistant_message',
      'agent_run',
    ] as const) ||
    value.conversation_id !== conversationId
  ) {
    invalidResponse('后端 Agent 创建响应关联不符合约定。')
  }
  const userMessage = mapConversationMessage(value.user_message)
  const assistantMessage = mapConversationMessage(value.assistant_message)
  const agentRun = mapAgentRunDto(value.agent_run)
  const expectedDocumentId = params.documentId ?? null
  const expectedLanguage = params.responseLanguage.trim().toLowerCase() || 'auto'
  if (
    userMessage.conversationId !== conversationId ||
    assistantMessage.conversationId !== conversationId ||
    agentRun.conversationId !== conversationId ||
    userMessage.id !== agentRun.userMessageId ||
    assistantMessage.id !== agentRun.assistantMessageId ||
    userMessage.role !== 'user' ||
    assistantMessage.role !== 'assistant' ||
    userMessage.status !== 'completed' ||
    userMessage.content !== params.content ||
    agentRun.clientRunId !== params.clientRunId.trim() ||
    agentRun.requestedDocumentId !== expectedDocumentId ||
    agentRun.requestedResponseLanguage !== expectedLanguage
  ) {
    invalidResponse('后端 Agent 创建响应关联不符合约定。')
  }
  const assistantMatchesRun =
    ((agentRun.status === 'queued' || agentRun.status === 'processing') &&
      assistantMessage.status === 'generating') ||
    (agentRun.status === 'succeeded' &&
      assistantMessage.status === 'completed' &&
      assistantMessage.content === agentRun.result?.answer) ||
    (agentRun.status === 'failed' && assistantMessage.status === 'failed') ||
    (agentRun.status === 'canceled' && assistantMessage.status === 'interrupted')
  if (!assistantMatchesRun) {
    invalidResponse('后端 Agent 创建响应关联不符合约定。')
  }
  return { conversationId, userMessage, assistantMessage, agentRun }
}

export function isAgentCapabilityUnavailable(error: unknown): boolean {
  const apiError = toApiError(error)
  return apiError.status === 404 && apiError.code === undefined
}

export async function createAgentRun(
  conversationId: number,
  params: CreateAgentRunParams,
  signal?: AbortSignal,
): Promise<AgentRunStartResult> {
  const response = await httpClient.post<unknown>(
    `/conversations/${conversationId}/agent-runs`,
    {
      content: params.content,
      client_run_id: params.clientRunId,
      document_id: params.documentId,
      response_language: params.responseLanguage,
    },
    { signal },
  )
  if (response.status !== 200 && response.status !== 202) invalidResponse()
  const result = mapCreateAgentRunResponse(response.data, conversationId, params)
  if (response.status === 202 && result.agentRun.status !== 'queued') invalidResponse()
  return result
}

export async function getAgentRun(runId: number, signal?: AbortSignal): Promise<AgentRun> {
  const response = await httpClient.get<unknown>(`/agent-runs/${runId}`, { signal })
  if (response.status !== 200) invalidResponse()
  return mapAgentRunEnvelope(response.data, { runId })
}

export async function getActiveAgentRun(
  conversationId: number,
  signal?: AbortSignal,
): Promise<AgentRun | null> {
  const response = await httpClient.get<unknown>(
    `/conversations/${conversationId}/agent-runs/active`,
    { signal },
  )
  if (response.status === 204) return null
  if (response.status !== 200) invalidResponse()
  return mapAgentRunEnvelope(response.data, { conversationId, active: true })
}

export async function getLatestAgentRun(
  conversationId: number,
  signal?: AbortSignal,
): Promise<AgentRun | null> {
  const response = await httpClient.get<unknown>(
    `/conversations/${conversationId}/agent-runs/latest`,
    { signal },
  )
  if (response.status === 204) return null
  if (response.status !== 200) invalidResponse()
  return mapAgentRunEnvelope(response.data, { conversationId })
}

export async function cancelAgentRun(runId: number, signal?: AbortSignal): Promise<AgentRun> {
  const response = await httpClient.post<unknown>(`/agent-runs/${runId}/cancel`, undefined, {
    signal,
  })
  if (response.status !== 200) invalidResponse()
  const run = mapAgentRunEnvelope(response.data, { runId })
  if (run.status !== 'canceled') invalidResponse()
  return run
}

export type { CreateAgentRunParams }
