import type {
  AnswerResponseLanguage,
  GroundedAnswerSource,
} from '../../../entities/answer/model/grounded-answer'
import type {
  Conversation,
  ConversationCursor,
  ConversationCursorPage,
  ConversationMessage,
  ConversationMessageCursorPage,
  ConversationMessagePage,
  ConversationPage,
  ConversationAnswerJob,
  ConversationAnswerJobStatus,
  ConversationAnswerStartResult,
} from '../../../entities/conversation/model/conversation'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

interface ConversationDto {
  id: number
  title: string
  status: 'active' | 'archived'
  last_message_at: string | null
  created_at: string
  updated_at: string
}

interface MessageDto {
  id: number
  conversation_id: number
  sequence_no: number
  role: 'user' | 'assistant'
  content: string
  status: 'completed' | 'generating' | 'failed' | 'interrupted'
  client_message_id: string | null
  created_at: string
  updated_at: string
}

interface PaginationDto {
  page: number
  page_size: number
  total: number
  total_pages: number
}

interface CursorDto {
  next: string | null
  has_more: boolean
}

interface AnswerJobDto {
  id: number
  document_id: number | null
  conversation_id: number | null
  user_message_id: number | null
  assistant_message_id: number | null
  query: string
  top_k: number
  requested_response_language: AnswerResponseLanguage
  status: ConversationAnswerJobStatus
  cancelable: boolean
  attempt_count: number
  error_code?: string
  error_message: string | null
  result: AnswerJobResultDto | null
  next_attempt_at: string
  created_at: string
  updated_at: string
  started_at: string | null
  completed_at: string | null
}

interface AnswerSourceDto {
  citation: number
  chunk_id: number
  document_id: number
  chunk_index: number
  title: string | null
  original_name: string
  page_start: number | null
  page_end: number | null
  similarity: number
}

interface AnswerUsageDto {
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
}

interface AnswerJobResultDto {
  answer: string
  response_language: Exclude<AnswerResponseLanguage, 'auto'>
  sources: AnswerSourceDto[]
  usage: AnswerUsageDto
}

export interface ListConversationsParams {
  page: number
  pageSize: number
}

export interface ListConversationMessagesParams {
  page: number
  pageSize: number
  order: 'asc' | 'desc'
}

export interface ListConversationCursorParams {
  pageSize: number
  cursor?: string
}

export interface UpdateConversationParams {
  title?: string
  status?: Conversation['status']
}

export interface QueueConversationAnswerParams {
  content: string
  clientMessageId: string
  documentId?: number
  topK: number
  responseLanguage: AnswerResponseLanguage
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function isNonNegativeInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isNullablePositiveInteger(value: unknown): value is number | null {
  return value === null || isPositiveInteger(value)
}

function isDateTime(value: unknown): value is string {
  return typeof value === 'string' && !Number.isNaN(Date.parse(value))
}

function isNullableDateTime(value: unknown): value is string | null {
  return value === null || isDateTime(value)
}

function isPagination(value: unknown): value is PaginationDto {
  if (!isRecord(value)) return false
  return (
    isPositiveInteger(value.page) &&
    isPositiveInteger(value.page_size) &&
    typeof value.total === 'number' &&
    Number.isSafeInteger(value.total) &&
    value.total >= 0 &&
    typeof value.total_pages === 'number' &&
    Number.isSafeInteger(value.total_pages) &&
    value.total_pages >= 0
  )
}

function isCursor(value: unknown): value is CursorDto {
  if (!isRecord(value) || typeof value.has_more !== 'boolean') return false
  const nextIsValid =
    value.next === null || (typeof value.next === 'string' && value.next.trim().length > 0)
  return nextIsValid && value.has_more === (value.next !== null)
}

function isConversation(value: unknown): value is ConversationDto {
  if (!isRecord(value)) return false
  return (
    isPositiveInteger(value.id) &&
    typeof value.title === 'string' &&
    value.title.trim().length > 0 &&
    (value.status === 'active' || value.status === 'archived') &&
    isNullableDateTime(value.last_message_at) &&
    isDateTime(value.created_at) &&
    isDateTime(value.updated_at)
  )
}

function isMessage(value: unknown): value is MessageDto {
  if (!isRecord(value)) return false
  return (
    isPositiveInteger(value.id) &&
    isPositiveInteger(value.conversation_id) &&
    isPositiveInteger(value.sequence_no) &&
    (value.role === 'user' || value.role === 'assistant') &&
    typeof value.content === 'string' &&
    value.content.trim().length > 0 &&
    (value.status === 'completed' ||
      value.status === 'generating' ||
      value.status === 'failed' ||
      value.status === 'interrupted') &&
    (value.client_message_id === null || typeof value.client_message_id === 'string') &&
    isDateTime(value.created_at) &&
    isDateTime(value.updated_at)
  )
}

function isAnswerJobStatus(value: unknown): value is ConversationAnswerJobStatus {
  return (
    value === 'queued' ||
    value === 'processing' ||
    value === 'succeeded' ||
    value === 'failed' ||
    value === 'canceled'
  )
}

function isAnswerResponseLanguage(value: unknown): value is AnswerResponseLanguage {
  return value === 'auto' || value === 'zh' || value === 'en'
}

function isResolvedAnswerLanguage(
  value: unknown,
): value is Exclude<AnswerResponseLanguage, 'auto'> {
  return value === 'zh' || value === 'en'
}

function isAnswerSource(value: unknown): value is AnswerSourceDto {
  if (!isRecord(value)) return false
  const pageRangeIsValid =
    (value.page_start === null && value.page_end === null) ||
    (isPositiveInteger(value.page_start) &&
      (value.page_end === null ||
        (isPositiveInteger(value.page_end) && value.page_end >= value.page_start)))
  return (
    isPositiveInteger(value.citation) &&
    isPositiveInteger(value.chunk_id) &&
    isPositiveInteger(value.document_id) &&
    isNonNegativeInteger(value.chunk_index) &&
    (value.title === null || typeof value.title === 'string') &&
    typeof value.original_name === 'string' &&
    value.original_name.trim().length > 0 &&
    pageRangeIsValid &&
    typeof value.similarity === 'number' &&
    Number.isFinite(value.similarity) &&
    value.similarity >= -1 &&
    value.similarity <= 1
  )
}

function isAnswerUsage(value: unknown): value is AnswerUsageDto {
  if (!isRecord(value)) return false
  return (
    isNonNegativeInteger(value.prompt_tokens) &&
    isNonNegativeInteger(value.completion_tokens) &&
    isNonNegativeInteger(value.total_tokens) &&
    value.total_tokens === value.prompt_tokens + value.completion_tokens
  )
}

function isAnswerJobResult(value: unknown): value is AnswerJobResultDto {
  if (!isRecord(value)) return false
  return (
    typeof value.answer === 'string' &&
    value.answer.trim().length > 0 &&
    isResolvedAnswerLanguage(value.response_language) &&
    Array.isArray(value.sources) &&
    value.sources.every(isAnswerSource) &&
    value.sources.every((source, index) => source.citation === index + 1) &&
    isAnswerUsage(value.usage)
  )
}

function isAnswerJob(value: unknown): value is AnswerJobDto {
  if (!isRecord(value)) return false
  const structurallyValid =
    isPositiveInteger(value.id) &&
    isNullablePositiveInteger(value.document_id) &&
    isPositiveInteger(value.conversation_id) &&
    isPositiveInteger(value.user_message_id) &&
    isPositiveInteger(value.assistant_message_id) &&
    typeof value.query === 'string' &&
    value.query.trim().length > 0 &&
    isPositiveInteger(value.top_k) &&
    value.top_k <= 20 &&
    isAnswerResponseLanguage(value.requested_response_language) &&
    isAnswerJobStatus(value.status) &&
    typeof value.cancelable === 'boolean' &&
    isNonNegativeInteger(value.attempt_count) &&
    (value.error_code === undefined || typeof value.error_code === 'string') &&
    (value.error_message === null || typeof value.error_message === 'string') &&
    (value.result === null || isAnswerJobResult(value.result)) &&
    isDateTime(value.next_attempt_at) &&
    isDateTime(value.created_at) &&
    isDateTime(value.updated_at) &&
    isNullableDateTime(value.started_at) &&
    (value.completed_at === null || isDateTime(value.completed_at))
  if (!structurallyValid) return false

  const status = value.status as ConversationAnswerJobStatus
  const terminal = status === 'succeeded' || status === 'failed' || status === 'canceled'
  return (
    value.cancelable === (status === 'queued') &&
    (status === 'succeeded' ? value.result !== null : value.result === null) &&
    (terminal ? value.completed_at !== null : value.completed_at === null)
  )
}

function mapConversation(source: ConversationDto): Conversation {
  return {
    id: source.id,
    title: source.title,
    status: source.status,
    lastMessageAt: source.last_message_at ? new Date(source.last_message_at) : null,
    createdAt: new Date(source.created_at),
    updatedAt: new Date(source.updated_at),
  }
}

function mapMessage(source: MessageDto): ConversationMessage {
  return {
    id: source.id,
    conversationId: source.conversation_id,
    sequenceNo: source.sequence_no,
    role: source.role,
    content: source.content,
    status: source.status,
    clientMessageId: source.client_message_id,
    createdAt: new Date(source.created_at),
    updatedAt: new Date(source.updated_at),
  }
}

function mapAnswerJob(source: AnswerJobDto): ConversationAnswerJob {
  return {
    id: source.id,
    documentId: source.document_id,
    conversationId: source.conversation_id as number,
    userMessageId: source.user_message_id as number,
    assistantMessageId: source.assistant_message_id as number,
    query: source.query,
    topK: source.top_k,
    requestedResponseLanguage: source.requested_response_language,
    status: source.status,
    cancelable: source.cancelable,
    attemptCount: source.attempt_count,
    errorCode: source.error_code ?? null,
    errorMessage: source.error_message,
    result: source.result
      ? {
          answer: source.result.answer,
          responseLanguage: source.result.response_language,
          sources: source.result.sources.map(mapAnswerSource),
          usage: {
            promptTokens: source.result.usage.prompt_tokens,
            completionTokens: source.result.usage.completion_tokens,
            totalTokens: source.result.usage.total_tokens,
          },
        }
      : null,
    nextAttemptAt: new Date(source.next_attempt_at),
    createdAt: new Date(source.created_at),
    updatedAt: new Date(source.updated_at),
    startedAt: source.started_at ? new Date(source.started_at) : null,
    completedAt: source.completed_at ? new Date(source.completed_at) : null,
  }
}

function mapAnswerSource(source: AnswerSourceDto): GroundedAnswerSource {
  return {
    citation: source.citation,
    chunkId: source.chunk_id,
    documentId: source.document_id,
    chunkIndex: source.chunk_index,
    title: source.title,
    originalName: source.original_name,
    pageStart: source.page_start,
    pageEnd: source.page_end,
    similarity: source.similarity,
  }
}

function mapPagination(source: PaginationDto) {
  return {
    page: source.page,
    pageSize: source.page_size,
    total: source.total,
    totalPages: source.total_pages,
  }
}

function mapCursor(source: CursorDto): ConversationCursor {
  return { next: source.next, hasMore: source.has_more }
}

export function mapConversationPageResponse(data: unknown): ConversationPage {
  if (
    !isRecord(data) ||
    !Array.isArray(data.conversations) ||
    !data.conversations.every(isConversation) ||
    !isPagination(data.pagination)
  ) {
    throw new ApiError('invalid-response', '后端会话列表响应不符合约定。')
  }
  return {
    conversations: data.conversations.map(mapConversation),
    pagination: mapPagination(data.pagination),
  }
}

export function mapConversationMessagePageResponse(data: unknown): ConversationMessagePage {
  if (
    !isRecord(data) ||
    !isPositiveInteger(data.conversation_id) ||
    !Array.isArray(data.messages) ||
    !data.messages.every(isMessage) ||
    !isPagination(data.pagination)
  ) {
    throw new ApiError('invalid-response', '后端会话消息响应不符合约定。')
  }
  return {
    conversationId: data.conversation_id,
    messages: data.messages.map(mapMessage),
    pagination: mapPagination(data.pagination),
  }
}

export function mapConversationCursorResponse(data: unknown): ConversationCursorPage {
  if (
    !isRecord(data) ||
    !Array.isArray(data.conversations) ||
    !data.conversations.every(isConversation) ||
    !isCursor(data.cursor)
  ) {
    throw new ApiError('invalid-response', '后端会话游标响应不符合约定。')
  }
  return {
    conversations: data.conversations.map(mapConversation),
    cursor: mapCursor(data.cursor),
  }
}

export function mapConversationMessageCursorResponse(data: unknown): ConversationMessageCursorPage {
  if (!isRecord(data) || !Array.isArray(data.messages)) {
    throw new ApiError('invalid-response', '后端会话消息游标响应不符合约定。')
  }
  const messages = data.messages
  if (
    !isPositiveInteger(data.conversation_id) ||
    !messages.every(isMessage) ||
    !messages.every((message) => message.conversation_id === data.conversation_id) ||
    !messages.every(
      (message, index) => index === 0 || messages[index - 1].sequence_no < message.sequence_no,
    ) ||
    !isCursor(data.cursor)
  ) {
    throw new ApiError('invalid-response', '后端会话消息游标响应不符合约定。')
  }
  return {
    conversationId: data.conversation_id,
    messages: messages.map(mapMessage),
    cursor: mapCursor(data.cursor),
  }
}

export async function listConversations(
  params: ListConversationsParams,
  signal?: AbortSignal,
): Promise<ConversationPage> {
  const response = await httpClient.get<unknown>('/conversations', {
    params: { page: params.page, page_size: params.pageSize },
    signal,
  })
  return mapConversationPageResponse(response.data)
}

export async function listConversationsCursor(
  params: ListConversationCursorParams,
  signal?: AbortSignal,
): Promise<ConversationCursorPage> {
  const response = await httpClient.get<unknown>('/conversations/cursor', {
    params: { page_size: params.pageSize, cursor: params.cursor },
    signal,
  })
  return mapConversationCursorResponse(response.data)
}

export async function createConversation(
  title?: string,
  signal?: AbortSignal,
): Promise<Conversation> {
  const response = await httpClient.post<unknown>(
    '/conversations',
    { title: title ?? '' },
    { signal },
  )
  if (!isConversation(response.data)) {
    throw new ApiError('invalid-response', '后端会话创建响应不符合约定。')
  }
  return mapConversation(response.data)
}

export async function updateConversation(
  conversationId: number,
  params: UpdateConversationParams,
  signal?: AbortSignal,
): Promise<Conversation> {
  const response = await httpClient.patch<unknown>(
    '/conversations/' + conversationId,
    {
      title: params.title,
      status: params.status,
    },
    { signal },
  )
  if (!isConversation(response.data)) {
    throw new ApiError('invalid-response', '后端会话更新响应不符合约定。')
  }
  const conversation = mapConversation(response.data)
  if (conversation.id !== conversationId) {
    throw new ApiError('invalid-response', '后端会话更新响应与请求 ID 不一致。')
  }
  return conversation
}

export async function deleteConversation(
  conversationId: number,
  signal?: AbortSignal,
): Promise<void> {
  const response = await httpClient.delete('/conversations/' + conversationId, { signal })
  if (response.status !== 204) {
    throw new ApiError('invalid-response', '后端会话删除响应不符合约定。')
  }
}

export async function listConversationMessages(
  conversationId: number,
  params: ListConversationMessagesParams,
  signal?: AbortSignal,
): Promise<ConversationMessagePage> {
  const response = await httpClient.get<unknown>(`/conversations/${conversationId}/messages`, {
    params: { page: params.page, page_size: params.pageSize, order: params.order },
    signal,
  })
  return mapConversationMessagePageResponse(response.data)
}

export async function listConversationMessagesCursor(
  conversationId: number,
  params: ListConversationCursorParams,
  signal?: AbortSignal,
): Promise<ConversationMessageCursorPage> {
  const response = await httpClient.get<unknown>(
    '/conversations/' + conversationId + '/messages/cursor',
    {
      params: { page_size: params.pageSize, cursor: params.cursor },
      signal,
    },
  )
  const result = mapConversationMessageCursorResponse(response.data)
  if (result.conversationId !== conversationId) {
    throw new ApiError('invalid-response', '后端会话消息游标响应与请求会话不一致。')
  }
  return result
}

export async function appendUserMessage(
  conversationId: number,
  content: string,
  clientMessageId: string,
  signal?: AbortSignal,
): Promise<{ created: boolean; message: ConversationMessage }> {
  const response = await httpClient.post<unknown>(
    `/conversations/${conversationId}/messages`,
    { content, client_message_id: clientMessageId },
    { signal },
  )
  if (
    !isRecord(response.data) ||
    typeof response.data.created !== 'boolean' ||
    !isMessage(response.data.message)
  ) {
    throw new ApiError('invalid-response', '后端会话消息写入响应不符合约定。')
  }
  return { created: response.data.created, message: mapMessage(response.data.message) }
}

export function mapConversationAnswerStartResponse(data: unknown): ConversationAnswerStartResult {
  if (
    !isRecord(data) ||
    !isPositiveInteger(data.conversation_id) ||
    !isMessage(data.user_message) ||
    !isMessage(data.assistant_message) ||
    !isAnswerJob(data.answer_job)
  ) {
    throw new ApiError('invalid-response', '后端会话回答任务响应不符合约定。')
  }

  const conversationId = data.conversation_id
  const userMessage = mapMessage(data.user_message)
  const assistantMessage = mapMessage(data.assistant_message)
  const answerJob = mapAnswerJob(data.answer_job)
  if (
    userMessage.conversationId !== conversationId ||
    assistantMessage.conversationId !== conversationId ||
    answerJob.conversationId !== conversationId ||
    answerJob.userMessageId !== userMessage.id ||
    answerJob.assistantMessageId !== assistantMessage.id ||
    userMessage.role !== 'user' ||
    assistantMessage.role !== 'assistant'
  ) {
    throw new ApiError('invalid-response', '后端会话回答任务关联关系不符合约定。')
  }

  return {
    conversationId,
    userMessage,
    assistantMessage,
    answerJob,
  }
}

export async function queueConversationAnswer(
  conversationId: number,
  params: QueueConversationAnswerParams,
  signal?: AbortSignal,
): Promise<ConversationAnswerStartResult> {
  const response = await httpClient.post<unknown>(
    `/conversations/${conversationId}/answer-jobs`,
    {
      content: params.content,
      client_message_id: params.clientMessageId,
      document_id: params.documentId,
      top_k: params.topK,
      response_language: params.responseLanguage,
    },
    { signal },
  )
  const result = mapConversationAnswerStartResponse(response.data)
  if (result.conversationId !== conversationId) {
    throw new ApiError('invalid-response', '后端会话回答任务响应与请求会话不一致。')
  }
  return result
}

export async function getConversationAnswerJob(
  jobId: number,
  signal?: AbortSignal,
): Promise<ConversationAnswerJob> {
  const response = await httpClient.get<unknown>(`/answer-jobs/${jobId}`, { signal })
  if (!isAnswerJob(response.data)) {
    throw new ApiError('invalid-response', '后端回答任务响应不符合约定。')
  }
  const job = mapAnswerJob(response.data)
  if (job.id !== jobId) {
    throw new ApiError('invalid-response', '后端回答任务响应与请求 ID 不一致。')
  }
  return job
}

/** 查询指定会话最近一条回答任务；活动任务用于恢复轮询，终态任务用于恢复结果或重试。 */
export async function getLatestConversationAnswerJob(
  conversationId: number,
  signal?: AbortSignal,
): Promise<ConversationAnswerJob | null> {
  const response = await httpClient.get<unknown>(
    `/conversations/${conversationId}/answer-jobs/latest`,
    { signal },
  )
  if (response.status === 204) return null
  if (!isAnswerJob(response.data)) {
    throw new ApiError('invalid-response', '后端最近回答任务响应不符合约定。')
  }
  const job = mapAnswerJob(response.data)
  if (job.conversationId !== conversationId) {
    throw new ApiError('invalid-response', '后端最近回答任务响应与请求会话不一致。')
  }
  return job
}

/** 兼容只提供旧 active 端点的滚动部署后端。 */
export async function getActiveConversationAnswerJob(
  conversationId: number,
  signal?: AbortSignal,
): Promise<ConversationAnswerJob | null> {
  const response = await httpClient.get<unknown>(
    `/conversations/${conversationId}/answer-jobs/active`,
    { signal },
  )
  if (response.status === 204) return null
  if (!isAnswerJob(response.data)) {
    throw new ApiError('invalid-response', '后端活动回答任务响应不符合约定。')
  }
  const job = mapAnswerJob(response.data)
  if (job.conversationId !== conversationId) {
    throw new ApiError('invalid-response', '后端活动回答任务响应与请求会话不一致。')
  }
  if (job.status !== 'queued' && job.status !== 'processing') {
    throw new ApiError('invalid-response', '后端活动回答任务返回了非活动状态。')
  }
  return job
}

/** 取消当前用户仍处于 queued 状态的会话回答任务。 */
export async function cancelConversationAnswerJob(
  jobId: number,
  signal?: AbortSignal,
): Promise<ConversationAnswerJob> {
  const response = await httpClient.post<unknown>(`/answer-jobs/${jobId}/cancel`, undefined, {
    signal,
  })
  if (!isAnswerJob(response.data)) {
    throw new ApiError('invalid-response', '后端取消回答任务响应不符合约定。')
  }
  const job = mapAnswerJob(response.data)
  if (job.id !== jobId) {
    throw new ApiError('invalid-response', '后端取消回答任务响应与请求 ID 不一致。')
  }
  return job
}
