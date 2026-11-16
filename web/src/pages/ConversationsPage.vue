<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type {
  AnswerResponseLanguage,
  GroundedAnswerSource,
} from '../entities/answer/model/grounded-answer'
import type {
  Conversation,
  ConversationAnswerJob,
  ConversationAnswerJobStatus,
  ConversationCursor,
  ConversationMessage,
  ConversationPagination,
} from '../entities/conversation/model/conversation'
import {
  allDocumentsScope,
  documentIdFromScope,
  type DocumentScope,
} from '../entities/document/model/document-scope'
import {
  appendUserMessage,
  cancelConversationAnswerJob,
  createConversation,
  deleteConversation,
  getActiveConversationAnswerJob,
  getConversationAnswerJob,
  getLatestConversationAnswerJob,
  listConversationMessages,
  listConversationMessagesCursor,
  listConversations,
  listConversationsCursor,
  queueConversationAnswer,
  updateConversation,
} from '../features/conversations/api/conversation-api'
import AgentRunPanel from '../features/agents/ui/AgentRunPanel.vue'
import { useAgentRunWorkspace } from '../features/agents/model/use-agent-run-workspace'
import DocumentScopePicker from '../features/documents/ui/DocumentScopePicker.vue'
import { toApiError } from '../shared/api/api-error'
import { capacityFailureFromApiError, type CapacityFailure } from '../shared/api/capacity-error'
import PageHeader from '../shared/ui/PageHeader.vue'

const conversations = ref<Conversation[]>([])
const conversationCursor = ref<ConversationCursor>({ next: null, hasMore: false })
const conversationLegacyPagination = ref<ConversationPagination>({
  page: 1,
  pageSize: 20,
  total: 0,
  totalPages: 0,
})
const conversationPaginationMode = ref<'cursor' | 'legacy' | null>(null)
const selectedConversation = ref<Conversation | null>(null)
const messages = ref<ConversationMessage[]>([])
const messageCursor = ref<ConversationCursor>({ next: null, hasMore: false })
const messageLegacyPagination = ref<ConversationPagination>({
  page: 1,
  pageSize: 50,
  total: 0,
  totalPages: 0,
})
const messagePaginationMode = ref<'cursor' | 'legacy' | null>(null)
const draft = ref('')
const loading = ref(true)
const loadingMoreConversations = ref(false)
const loadingEarlierMessages = ref(false)
const creating = ref(false)
const renaming = ref(false)
const updatingStatus = ref(false)
const deleting = ref(false)
const editingTitle = ref(false)
const titleDraft = ref('')
const deleteConfirmationConversationId = ref<number | null>(null)
const sending = ref(false)
type ComposerMode = 'message' | 'answer' | 'agent'
const composerMode = ref<ComposerMode>('message')
const documentScope = ref<DocumentScope>(allDocumentsScope())
const topK = ref(5)
const responseLanguage = ref<AnswerResponseLanguage>('auto')
const answerJobStatus = ref<ConversationAnswerJobStatus | null>(null)
const answerJob = ref<ConversationAnswerJob | null>(null)
const canceling = ref(false)
const retrying = ref(false)
const errorMessage = ref<string | null>(null)
const executionConflictUnresolved = ref(false)
const capacityFailure = ref<CapacityFailure | null>(null)
const capacityRetryRemaining = ref(0)
let answerPollTimer: ReturnType<typeof setTimeout> | null = null
let answerPollGeneration = 0
let conversationSelectionGeneration = 0
let conversationListGeneration = 0
let conversationLoadedMore = false
let messageLoadedMore = false
let capacityTimer: ReturnType<typeof setTimeout> | null = null

const canRetryAnswer = computed(
  () => answerJob.value?.status === 'failed' || answerJob.value?.status === 'canceled',
)
const canCancelAnswer = computed(() => answerJob.value?.status === 'queued')
const answerBusy = computed(
  () => answerJobStatus.value === 'queued' || answerJobStatus.value === 'processing',
)
const hasMoreConversations = computed(() =>
  conversationPaginationMode.value === 'cursor'
    ? conversationCursor.value.hasMore && conversationCursor.value.next !== null
    : conversationPaginationMode.value === 'legacy' &&
      conversationLegacyPagination.value.page < conversationLegacyPagination.value.totalPages,
)
const hasEarlierMessages = computed(() =>
  messagePaginationMode.value === 'cursor'
    ? messageCursor.value.hasMore && messageCursor.value.next !== null
    : messagePaginationMode.value === 'legacy' &&
      messageLegacyPagination.value.page < messageLegacyPagination.value.totalPages,
)
const conversationArchived = computed(() => selectedConversation.value?.status === 'archived')
const conversationMutationBusy = computed(
  () => renaming.value || updatingStatus.value || deleting.value,
)

const conversationPageSize = 20
const messagePageSize = 50

function conversationSelectionIsCurrent(conversationId: number, generation: number): boolean {
  return (
    generation === conversationSelectionGeneration &&
    selectedConversation.value?.id === conversationId
  )
}

const agentWorkspace = useAgentRunWorkspace({
  isSelectionCurrent: (context) =>
    conversationSelectionIsCurrent(context.conversationId, context.selectionGeneration),
  onTerminal: async (context) => {
    await refreshLatestMessagesForSelection(context.conversationId, context.selectionGeneration)
    await refreshConversationListForSelection(context.conversationId, context.selectionGeneration)
  },
})

const {
  availability: agentAvailability,
  bindConversation: bindAgentConversation,
  canCancel: canCancelAgent,
  cancel: cancelAgent,
  capacityFailure: agentCapacityFailure,
  clearConversation: clearAgentConversation,
  errorMessage: agentErrorMessage,
  isActive: agentBusy,
  isCoolingDown: agentCoolingDown,
  manualRestore: restoreAgentManually,
  manualRestoreRequired: agentManualRestoreRequired,
  queue: queueAgent,
  requestId: agentRequestId,
  restoreActive: restoreActiveAgent,
  restoreLatest: restoreLatestAgent,
  retryAfterSeconds: agentRetryRemaining,
  run: agentRun,
  state: agentWorkspaceState,
} = agentWorkspace

const agentActionBusy = computed(
  () =>
    agentWorkspaceState.value === 'restoring' ||
    agentWorkspaceState.value === 'queueing' ||
    agentWorkspaceState.value === 'canceling',
)
const executionBusy = computed(
  () =>
    answerBusy.value ||
    agentBusy.value ||
    agentActionBusy.value ||
    executionConflictUnresolved.value,
)
const composerCoolingDown = computed(
  () =>
    (composerMode.value === 'answer' && capacityRetryRemaining.value > 0) ||
    (composerMode.value === 'agent' && agentCoolingDown.value),
)

watch(agentAvailability, (availability) => {
  if (availability === 'unavailable' && composerMode.value === 'agent') {
    composerMode.value = 'message'
  }
})

function clearCapacityFailure(): void {
  if (capacityTimer !== null) {
    clearTimeout(capacityTimer)
    capacityTimer = null
  }
  capacityFailure.value = null
  capacityRetryRemaining.value = 0
}

function activateCapacityFailure(failure: CapacityFailure): void {
  clearCapacityFailure()
  capacityFailure.value = failure
  capacityRetryRemaining.value = failure.retryAfterSeconds

  const tick = (): void => {
    capacityRetryRemaining.value = Math.max(0, capacityRetryRemaining.value - 1)
    if (capacityRetryRemaining.value === 0) {
      capacityTimer = null
      return
    }
    capacityTimer = setTimeout(tick, 1000)
  }

  if (capacityRetryRemaining.value > 0) {
    capacityTimer = setTimeout(tick, 1000)
  }
}

function showRequestError(error: unknown, fallbackMessage: string): void {
  const apiError = toApiError(error)
  const capacity = capacityFailureFromApiError(apiError, 5)
  if (capacity) {
    activateCapacityFailure(capacity)
    errorMessage.value = null
    return
  }
  errorMessage.value = apiError.message || fallbackMessage
}

function invalidateAnswerPolling(): void {
  answerPollGeneration += 1
  if (answerPollTimer !== null) {
    clearTimeout(answerPollTimer)
    answerPollTimer = null
  }
}

function stopAnswerPolling(): void {
  invalidateAnswerPolling()
  answerJobStatus.value = null
  answerJob.value = null
}

function mergeMessages(nextMessages: ConversationMessage[]): void {
  messages.value = [
    ...messages.value.filter((message) => !nextMessages.some((next) => next.id === message.id)),
    ...nextMessages,
  ].sort((left, right) => left.sequenceNo - right.sequenceNo)
}

function mergeConversationPage(nextConversations: Conversation[], prioritizeNext = false): void {
  const ordered = prioritizeNext
    ? [...nextConversations, ...conversations.value]
    : [...conversations.value, ...nextConversations]
  const seen = new Set<number>()
  conversations.value = ordered
    .filter((conversation) => {
      if (seen.has(conversation.id)) return false
      seen.add(conversation.id)
      return true
    })
    .sort((left, right) => {
      const leftTime = left.updatedAt.getTime()
      const rightTime = right.updatedAt.getTime()
      return rightTime - leftTime || right.id - left.id
    })
}

async function getRestorableConversationAnswerJob(
  conversationId: number,
): Promise<ConversationAnswerJob | null> {
  try {
    return await getLatestConversationAnswerJob(conversationId)
  } catch (error) {
    if (toApiError(error).kind !== 'not-found') throw error
  }

  try {
    return await getActiveConversationAnswerJob(conversationId)
  } catch (error) {
    if (toApiError(error).kind === 'not-found') return null
    throw error
  }
}

async function fetchConversationHead() {
  if (conversationPaginationMode.value === 'legacy') {
    const result = await listConversations({ page: 1, pageSize: conversationPageSize })
    return {
      mode: 'legacy' as const,
      conversations: result.conversations,
      cursor: null,
      pagination: result.pagination,
    }
  }
  if (conversationPaginationMode.value === 'cursor') {
    const result = await listConversationsCursor({ pageSize: conversationPageSize })
    return {
      mode: 'cursor' as const,
      conversations: result.conversations,
      cursor: result.cursor,
      pagination: null,
    }
  }

  try {
    const result = await listConversationsCursor({ pageSize: conversationPageSize })
    return {
      mode: 'cursor' as const,
      conversations: result.conversations,
      cursor: result.cursor,
      pagination: null,
    }
  } catch (error) {
    const apiError = toApiError(error)
    const legacyRouteMiss =
      apiError.kind === 'not-found' ||
      (apiError.status === 400 && apiError.code === 'invalid_conversation_id')
    if (!legacyRouteMiss) throw error
    const result = await listConversations({ page: 1, pageSize: conversationPageSize })
    return {
      mode: 'legacy' as const,
      conversations: result.conversations,
      cursor: null,
      pagination: result.pagination,
    }
  }
}

async function fetchMessageHead(conversationId: number) {
  if (messagePaginationMode.value === 'legacy') {
    const result = await listConversationMessages(conversationId, {
      page: 1,
      pageSize: messagePageSize,
      order: 'desc',
    })
    return {
      mode: 'legacy' as const,
      messages: result.messages,
      cursor: null,
      pagination: result.pagination,
    }
  }
  if (messagePaginationMode.value === 'cursor') {
    const result = await listConversationMessagesCursor(conversationId, {
      pageSize: messagePageSize,
    })
    return {
      mode: 'cursor' as const,
      messages: result.messages,
      cursor: result.cursor,
      pagination: null,
    }
  }

  try {
    const result = await listConversationMessagesCursor(conversationId, {
      pageSize: messagePageSize,
    })
    return {
      mode: 'cursor' as const,
      messages: result.messages,
      cursor: result.cursor,
      pagination: null,
    }
  } catch (error) {
    if (toApiError(error).kind !== 'not-found') throw error
    const result = await listConversationMessages(conversationId, {
      page: 1,
      pageSize: messagePageSize,
      order: 'desc',
    })
    return {
      mode: 'legacy' as const,
      messages: result.messages,
      cursor: null,
      pagination: result.pagination,
    }
  }
}

function applyAnswerJobParameters(job: ConversationAnswerJob): void {
  documentScope.value = job.documentId
    ? { kind: 'single', documentId: job.documentId }
    : allDocumentsScope()
  topK.value = job.topK
  responseLanguage.value = job.requestedResponseLanguage
}

function resetAnswerParameters(): void {
  documentScope.value = allDocumentsScope()
  topK.value = 5
  responseLanguage.value = 'auto'
}

async function refreshLatestMessagesForSelection(
  conversationId: number,
  selectionGeneration: number,
): Promise<void> {
  const result = await fetchMessageHead(conversationId)
  if (!conversationSelectionIsCurrent(conversationId, selectionGeneration)) return
  messagePaginationMode.value = result.mode
  const maxLoadedSequence = messages.value.reduce(
    (maximum, message) => Math.max(maximum, message.sequenceNo),
    0,
  )
  const headOldestSequence =
    result.messages.length > 0
      ? Math.min(...result.messages.map((message) => message.sequenceNo))
      : null
  const cursorGapDetected =
    messagePaginationMode.value === 'cursor' &&
    maxLoadedSequence > 0 &&
    headOldestSequence !== null &&
    headOldestSequence > maxLoadedSequence + 1
  mergeMessages(result.messages)
  if (
    messagePaginationMode.value === 'cursor' &&
    result.cursor &&
    (maxLoadedSequence === 0 || cursorGapDetected)
  ) {
    messageCursor.value = result.cursor
  }
  if (messagePaginationMode.value === 'legacy' && !messageLoadedMore && result.pagination) {
    messageLegacyPagination.value = result.pagination
  }
}

async function pollAnswerJob(
  jobId: number,
  generation: number,
  conversationId: number,
  selectionGeneration: number,
): Promise<void> {
  if (
    generation !== answerPollGeneration ||
    !conversationSelectionIsCurrent(conversationId, selectionGeneration)
  )
    return
  try {
    const job = await getConversationAnswerJob(jobId)
    if (
      generation !== answerPollGeneration ||
      !conversationSelectionIsCurrent(conversationId, selectionGeneration)
    )
      return
    answerJob.value = job
    answerJobStatus.value = job.status
    if (job.status === 'succeeded' || job.status === 'failed' || job.status === 'canceled') {
      await refreshLatestMessagesForSelection(conversationId, selectionGeneration)
      if (
        generation !== answerPollGeneration ||
        !conversationSelectionIsCurrent(conversationId, selectionGeneration)
      )
        return
      if (job.status !== 'succeeded') {
        errorMessage.value =
          job.status === 'canceled' ? '回答任务已取消。' : (job.errorMessage ?? '回答任务未完成。')
      } else {
        errorMessage.value = null
      }
      answerPollTimer = null
      return
    }
    answerPollTimer = setTimeout(() => {
      void pollAnswerJob(jobId, generation, conversationId, selectionGeneration)
    }, 1500)
  } catch (error) {
    if (
      generation !== answerPollGeneration ||
      !conversationSelectionIsCurrent(conversationId, selectionGeneration)
    )
      return
    answerPollTimer = null
    showRequestError(error, '回答任务状态获取失败。')
  }
}

function startAnswerPolling(
  job: ConversationAnswerJob,
  conversationId: number,
  selectionGeneration = conversationSelectionGeneration,
): void {
  stopAnswerPolling()
  const generation = answerPollGeneration
  answerJob.value = job
  answerJobStatus.value = job.status
  void pollAnswerJob(job.id, generation, conversationId, selectionGeneration)
}

function applyAnswerJobSnapshot(
  job: ConversationAnswerJob | null,
  conversationId: number,
  selectionGeneration: number,
): void {
  if (!conversationSelectionIsCurrent(conversationId, selectionGeneration)) return
  invalidateAnswerPolling()
  answerJob.value = job
  answerJobStatus.value = job?.status ?? null
  if (!job) return
  applyAnswerJobParameters(job)
  if (job.status === 'queued' || job.status === 'processing') {
    startAnswerPolling(job, conversationId, selectionGeneration)
  }
}

async function getActiveAnswerForConflict(
  conversationId: number,
): Promise<ConversationAnswerJob | null> {
  try {
    return await getActiveConversationAnswerJob(conversationId)
  } catch (error) {
    const apiError = toApiError(error)
    if (apiError.kind === 'not-found' && apiError.code !== 'conversation_not_found') return null
    throw error
  }
}

function isExecutionConflict(error: unknown): boolean {
  const apiError = toApiError(error)
  return (
    apiError.kind === 'conflict' &&
    (apiError.code === 'conversation_execution_active' ||
      apiError.code === 'conversation_answer_active')
  )
}

async function reconcileExecutionConflict(
  conversationId: number,
  selectionGeneration: number,
): Promise<void> {
  if (!conversationSelectionIsCurrent(conversationId, selectionGeneration)) return
  executionConflictUnresolved.value = true
  errorMessage.value = null
  const [answerActiveResult, agentActiveResult] = await Promise.allSettled([
    getActiveAnswerForConflict(conversationId),
    restoreActiveAgent(),
  ])
  if (!conversationSelectionIsCurrent(conversationId, selectionGeneration)) return

  if (answerActiveResult.status === 'rejected') {
    showRequestError(answerActiveResult.reason, '活动回答状态恢复失败。')
    return
  }
  if (
    agentActiveResult.status === 'rejected' ||
    agentActiveResult.value.kind === 'failed' ||
    agentActiveResult.value.kind === 'stale'
  ) {
    errorMessage.value = '活动 Agent 状态尚未确认，已暂停新的提交。'
    return
  }

  const activeAnswer = answerActiveResult.value
  if (activeAnswer) applyAnswerJobSnapshot(activeAnswer, conversationId, selectionGeneration)
  const activeAgent =
    agentActiveResult.status === 'fulfilled' && agentActiveResult.value.kind === 'active'
      ? agentActiveResult.value.run
      : null

  if (activeAnswer && activeAgent) {
    executionConflictUnresolved.value = false
    errorMessage.value = '同一会话返回了多个活动执行，已暂停新的提交。'
    return
  }
  if (activeAnswer || activeAgent) {
    executionConflictUnresolved.value = false
    errorMessage.value = '已恢复当前会话的活动执行，不会自动重复提交。'
    return
  }

  const [messagesResult, answerLatestResult, agentLatestResult] = await Promise.allSettled([
    refreshLatestMessagesForSelection(conversationId, selectionGeneration),
    getLatestConversationAnswerJob(conversationId),
    restoreLatestAgent(),
  ])
  if (!conversationSelectionIsCurrent(conversationId, selectionGeneration)) return
  let reconciliationFailed = false
  if (answerLatestResult.status === 'fulfilled') {
    applyAnswerJobSnapshot(answerLatestResult.value, conversationId, selectionGeneration)
  } else {
    const apiError = toApiError(answerLatestResult.reason)
    if (apiError.kind !== 'not-found' || apiError.code === 'conversation_not_found') {
      reconciliationFailed = true
      showRequestError(answerLatestResult.reason, '执行冲突后的回答状态恢复失败。')
    }
  }
  if (messagesResult.status === 'rejected') {
    reconciliationFailed = true
    showRequestError(messagesResult.reason, '执行冲突后的消息刷新失败。')
  }
  if (
    agentLatestResult.status === 'rejected' ||
    agentLatestResult.value.kind === 'failed' ||
    agentLatestResult.value.kind === 'stale'
  ) {
    reconciliationFailed = true
    errorMessage.value = '执行冲突后的 Agent 状态恢复失败。'
  }
  if (reconciliationFailed) return
  executionConflictUnresolved.value = false
  if (!errorMessage.value) {
    errorMessage.value = '已刷新会话执行状态，不会自动重复提交。'
  }
}

async function handleReconcileExecution(): Promise<void> {
  const conversation = selectedConversation.value
  if (!conversation) return
  await reconcileExecutionConflict(conversation.id, conversationSelectionGeneration)
}

async function refreshConversations(selectFirst = true): Promise<void> {
  const listGeneration = ++conversationListGeneration
  loading.value = true
  errorMessage.value = null
  try {
    const result = await fetchConversationHead()
    if (listGeneration !== conversationListGeneration) return
    conversationPaginationMode.value = result.mode
    conversations.value = []
    mergeConversationPage(result.conversations)
    if (conversationPaginationMode.value === 'cursor' && result.cursor) {
      conversationCursor.value = result.cursor
    } else if (conversationPaginationMode.value === 'legacy' && result.pagination) {
      conversationLegacyPagination.value = result.pagination
    }
    conversationLoadedMore = false
    if (selectFirst && !selectedConversation.value && conversations.value.length > 0) {
      await selectConversation(conversations.value[0])
    }
  } catch (error) {
    showRequestError(error, '会话列表加载失败。')
  } finally {
    loading.value = false
  }
}

async function loadMoreConversations(): Promise<void> {
  if (!hasMoreConversations.value || loadingMoreConversations.value) return
  const listGeneration = conversationListGeneration
  loadingMoreConversations.value = true
  try {
    if (conversationPaginationMode.value === 'cursor') {
      const cursor = conversationCursor.value.next
      if (!cursor) return
      const result = await listConversationsCursor({ pageSize: conversationPageSize, cursor })
      if (listGeneration !== conversationListGeneration) return
      mergeConversationPage(result.conversations)
      conversationCursor.value = result.cursor
    } else if (conversationPaginationMode.value === 'legacy') {
      const result = await listConversations({
        page: conversationLegacyPagination.value.page + 1,
        pageSize: conversationPageSize,
      })
      if (listGeneration !== conversationListGeneration) return
      mergeConversationPage(result.conversations)
      conversationLegacyPagination.value = result.pagination
    } else {
      return
    }
    conversationLoadedMore = true
  } catch (error) {
    showRequestError(error, '更多会话加载失败。')
  } finally {
    loadingMoreConversations.value = false
  }
}

async function selectConversation(conversation: Conversation): Promise<void> {
  stopAnswerPolling()
  const selectionGeneration = ++conversationSelectionGeneration
  executionConflictUnresolved.value = false
  selectedConversation.value = conversation
  bindAgentConversation({ conversationId: conversation.id, selectionGeneration })
  editingTitle.value = false
  titleDraft.value = conversation.title
  deleteConfirmationConversationId.value = null
  messages.value = []
  messageCursor.value = { next: null, hasMore: false }
  messageLegacyPagination.value = {
    page: 1,
    pageSize: messagePageSize,
    total: 0,
    totalPages: 0,
  }
  messagePaginationMode.value = null
  messageLoadedMore = false
  resetAnswerParameters()
  errorMessage.value = null

  const selectionIsCurrent = (): boolean =>
    conversationSelectionIsCurrent(conversation.id, selectionGeneration)

  try {
    const result = await fetchMessageHead(conversation.id)
    if (!selectionIsCurrent()) return
    messagePaginationMode.value = result.mode
    messages.value = [...result.messages].sort((left, right) => left.sequenceNo - right.sequenceNo)
    if (messagePaginationMode.value === 'cursor' && result.cursor) {
      messageCursor.value = result.cursor
    } else if (messagePaginationMode.value === 'legacy' && result.pagination) {
      messageLegacyPagination.value = result.pagination
    }
  } catch (error) {
    if (!selectionIsCurrent()) return
    showRequestError(error, '会话消息加载失败。')
    messages.value = []
    return
  }

  const [latestJobResult, agentRestoreResult] = await Promise.allSettled([
    getRestorableConversationAnswerJob(conversation.id),
    restoreLatestAgent(),
  ])
  if (!selectionIsCurrent()) return

  const latestJob = latestJobResult.status === 'fulfilled' ? latestJobResult.value : null
  if (latestJob) {
    answerJob.value = latestJob
    answerJobStatus.value = latestJob.status
    applyAnswerJobParameters(latestJob)
    if (latestJob.status === 'queued' || latestJob.status === 'processing') {
      startAnswerPolling(latestJob, conversation.id, selectionGeneration)
    }
  } else if (
    latestJobResult.status === 'rejected' &&
    toApiError(latestJobResult.reason).kind !== 'not-found'
  ) {
    showRequestError(latestJobResult.reason, '最近回答任务状态恢复失败。')
  }
  if (agentRestoreResult.status === 'fulfilled' && agentRestoreResult.value.kind === 'terminal') {
    await refreshLatestMessagesForSelection(conversation.id, selectionGeneration)
  }
}

async function loadEarlierMessages(): Promise<void> {
  const conversation = selectedConversation.value
  if (!conversation || !hasEarlierMessages.value || loadingEarlierMessages.value) return
  const conversationId = conversation.id
  const selectionGeneration = conversationSelectionGeneration
  loadingEarlierMessages.value = true
  try {
    if (messagePaginationMode.value === 'cursor') {
      const cursor = messageCursor.value.next
      if (!cursor) return
      const result = await listConversationMessagesCursor(conversationId, {
        pageSize: messagePageSize,
        cursor,
      })
      if (!conversationSelectionIsCurrent(conversationId, selectionGeneration)) return
      mergeMessages(result.messages)
      messageCursor.value = result.cursor
    } else if (messagePaginationMode.value === 'legacy') {
      const result = await listConversationMessages(conversationId, {
        page: messageLegacyPagination.value.page + 1,
        pageSize: messagePageSize,
        order: 'desc',
      })
      if (!conversationSelectionIsCurrent(conversationId, selectionGeneration)) return
      mergeMessages(result.messages)
      messageLegacyPagination.value = result.pagination
    } else {
      return
    }
    messageLoadedMore = true
  } catch (error) {
    if (!conversationSelectionIsCurrent(conversationId, selectionGeneration)) return
    showRequestError(error, '更早消息加载失败。')
  } finally {
    loadingEarlierMessages.value = false
  }
}

async function refreshConversationListForSelection(
  conversationId: number,
  selectionGeneration: number,
): Promise<void> {
  const listGeneration = ++conversationListGeneration
  try {
    const result = await fetchConversationHead()
    if (
      listGeneration !== conversationListGeneration ||
      !conversationSelectionIsCurrent(conversationId, selectionGeneration)
    )
      return
    conversationPaginationMode.value = result.mode
    mergeConversationPage(result.conversations, true)
    if (
      conversationPaginationMode.value === 'legacy' &&
      !conversationLoadedMore &&
      result.pagination
    ) {
      conversationLegacyPagination.value = result.pagination
    }
    const refreshed = conversations.value.find((conversation) => conversation.id === conversationId)
    if (refreshed) selectedConversation.value = refreshed
  } catch (error) {
    if (
      listGeneration !== conversationListGeneration ||
      !conversationSelectionIsCurrent(conversationId, selectionGeneration)
    )
      return
    showRequestError(error, '会话列表加载失败。')
  }
}

function beginRenameConversation(): void {
  const conversation = selectedConversation.value
  if (
    !conversation ||
    conversationMutationBusy.value ||
    sending.value ||
    canceling.value ||
    retrying.value ||
    agentActionBusy.value
  )
    return
  titleDraft.value = conversation.title
  editingTitle.value = true
  deleteConfirmationConversationId.value = null
  errorMessage.value = null
}

function cancelRenameConversation(): void {
  titleDraft.value = selectedConversation.value?.title ?? ''
  editingTitle.value = false
}

async function handleRenameConversation(): Promise<void> {
  const conversation = selectedConversation.value
  const title = titleDraft.value.trim()
  if (!conversation || conversationMutationBusy.value) return
  if (!title || [...title].length > 200) {
    errorMessage.value = '会话标题必须为 1–200 个字符。'
    return
  }

  const conversationId = conversation.id
  const selectionGeneration = conversationSelectionGeneration
  const selectionIsCurrent = (): boolean =>
    conversationSelectionIsCurrent(conversationId, selectionGeneration)
  renaming.value = true
  errorMessage.value = null
  try {
    const updated = await updateConversation(conversationId, { title })
    conversationListGeneration += 1
    mergeConversationPage([updated], true)
    if (!selectionIsCurrent()) return
    selectedConversation.value = updated
    titleDraft.value = updated.title
    editingTitle.value = false
  } catch (error) {
    if (!selectionIsCurrent()) return
    showRequestError(error, '会话重命名失败。')
  } finally {
    renaming.value = false
  }
}

async function handleConversationStatus(status: Conversation['status']): Promise<void> {
  const conversation = selectedConversation.value
  if (
    !conversation ||
    conversation.status === status ||
    conversationMutationBusy.value ||
    sending.value ||
    canceling.value ||
    retrying.value ||
    agentActionBusy.value
  )
    return

  const conversationId = conversation.id
  const selectionGeneration = conversationSelectionGeneration
  const selectionIsCurrent = (): boolean =>
    conversationSelectionIsCurrent(conversationId, selectionGeneration)
  updatingStatus.value = true
  errorMessage.value = null
  deleteConfirmationConversationId.value = null
  try {
    const updated = await updateConversation(conversationId, { status })
    conversationListGeneration += 1
    mergeConversationPage([updated], true)
    if (!selectionIsCurrent()) return
    selectedConversation.value = updated
    titleDraft.value = updated.title
    editingTitle.value = false
    if (status === 'archived') invalidateAnswerPolling()
  } catch (error) {
    if (!selectionIsCurrent()) return
    if (isExecutionConflict(error)) {
      await reconcileExecutionConflict(conversationId, selectionGeneration)
      return
    }
    showRequestError(error, status === 'archived' ? '会话归档失败。' : '会话恢复失败。')
  } finally {
    updatingStatus.value = false
  }
}

function requestDeleteConversation(): void {
  const conversation = selectedConversation.value
  if (
    !conversation ||
    conversationMutationBusy.value ||
    sending.value ||
    canceling.value ||
    retrying.value ||
    agentActionBusy.value
  )
    return
  deleteConfirmationConversationId.value = conversation.id
  editingTitle.value = false
  errorMessage.value = null
}

function cancelDeleteConversation(): void {
  deleteConfirmationConversationId.value = null
}

async function handleDeleteConversation(): Promise<void> {
  const conversation = selectedConversation.value
  if (
    !conversation ||
    deleteConfirmationConversationId.value !== conversation.id ||
    conversationMutationBusy.value ||
    sending.value ||
    canceling.value ||
    retrying.value ||
    agentActionBusy.value
  )
    return

  const conversationId = conversation.id
  const selectionGeneration = conversationSelectionGeneration
  const selectionIsCurrent = (): boolean =>
    conversationSelectionIsCurrent(conversationId, selectionGeneration)
  deleting.value = true
  errorMessage.value = null
  try {
    await deleteConversation(conversationId)
    const deletedIndex = conversations.value.findIndex((item) => item.id === conversationId)
    conversationListGeneration += 1
    conversations.value = conversations.value.filter((item) => item.id !== conversationId)
    if (deleteConfirmationConversationId.value === conversationId) {
      deleteConfirmationConversationId.value = null
    }
    if (!selectionIsCurrent()) return

    conversationSelectionGeneration += 1
    executionConflictUnresolved.value = false
    stopAnswerPolling()
    clearAgentConversation()
    const nextConversation =
      conversations.value[deletedIndex] ?? conversations.value[deletedIndex - 1] ?? null
    if (nextConversation) {
      await selectConversation(nextConversation)
    } else {
      selectedConversation.value = null
      messages.value = []
      messageCursor.value = { next: null, hasMore: false }
      messageLegacyPagination.value = {
        page: 1,
        pageSize: messagePageSize,
        total: 0,
        totalPages: 0,
      }
      messagePaginationMode.value = null
      messageLoadedMore = false
      titleDraft.value = ''
      editingTitle.value = false
      resetAnswerParameters()
      if (hasMoreConversations.value) await refreshConversations(true)
    }
  } catch (error) {
    if (!selectionIsCurrent()) return
    if (isExecutionConflict(error)) {
      await reconcileExecutionConflict(conversationId, selectionGeneration)
      return
    }
    showRequestError(error, '会话删除失败。')
  } finally {
    deleting.value = false
  }
}

async function handleCreateConversation(): Promise<void> {
  stopAnswerPolling()
  clearAgentConversation()
  executionConflictUnresolved.value = false
  const selectionGeneration = ++conversationSelectionGeneration
  conversationListGeneration += 1
  creating.value = true
  errorMessage.value = null
  try {
    const conversation = await createConversation()
    conversations.value = [conversation, ...conversations.value]
    messages.value = []
    messageCursor.value = { next: null, hasMore: false }
    messageLegacyPagination.value = {
      page: 1,
      pageSize: messagePageSize,
      total: 0,
      totalPages: 0,
    }
    messagePaginationMode.value = null
    messageLoadedMore = false
    selectedConversation.value = conversation
    bindAgentConversation({ conversationId: conversation.id, selectionGeneration })
    editingTitle.value = false
    titleDraft.value = conversation.title
    deleteConfirmationConversationId.value = null
    resetAnswerParameters()
    await restoreLatestAgent()
  } catch (error) {
    showRequestError(error, '会话创建失败。')
  } finally {
    creating.value = false
  }
}

async function handleSendMessage(): Promise<void> {
  const content = draft.value.trim()
  const conversation = selectedConversation.value
  if (
    !conversation ||
    conversation.status !== 'active' ||
    !content ||
    conversationMutationBusy.value ||
    sending.value ||
    executionBusy.value ||
    composerCoolingDown.value
  )
    return

  const conversationId = conversation.id
  const selectionGeneration = conversationSelectionGeneration
  const operationIsCurrent = (): boolean =>
    conversationSelectionIsCurrent(conversationId, selectionGeneration)
  sending.value = true
  errorMessage.value = null
  if (composerMode.value === 'answer') clearCapacityFailure()
  try {
    if (composerMode.value === 'agent') {
      const outcome = await queueAgent({
        content,
        clientRunId: crypto.randomUUID(),
        documentId: documentIdFromScope(documentScope.value),
        responseLanguage: responseLanguage.value,
      })
      if (!operationIsCurrent()) return
      if (outcome.kind === 'execution-conflict') {
        await reconcileExecutionConflict(conversationId, selectionGeneration)
        return
      }
      if (outcome.kind === 'unavailable') {
        composerMode.value = 'message'
        return
      }
      if (outcome.kind !== 'started') return
      mergeMessages([outcome.result.userMessage, outcome.result.assistantMessage])
      draft.value = ''
      await refreshConversationListForSelection(conversationId, selectionGeneration)
      return
    }

    const clientMessageId = crypto.randomUUID()
    if (composerMode.value === 'answer') {
      const result = await queueConversationAnswer(conversationId, {
        content,
        clientMessageId,
        documentId: documentIdFromScope(documentScope.value),
        topK: topK.value,
        responseLanguage: responseLanguage.value,
      })
      if (!operationIsCurrent()) return
      mergeMessages([result.userMessage, result.assistantMessage])
      applyAnswerJobParameters(result.answerJob)
      startAnswerPolling(result.answerJob, result.conversationId, selectionGeneration)
    } else {
      const result = await appendUserMessage(conversationId, content, clientMessageId)
      if (!operationIsCurrent()) return
      mergeMessages([result.message])
    }
    draft.value = ''
    await refreshConversationListForSelection(conversationId, selectionGeneration)
  } catch (error) {
    if (!operationIsCurrent()) return
    if (isExecutionConflict(error)) {
      await reconcileExecutionConflict(conversationId, selectionGeneration)
      return
    }
    showRequestError(error, '消息发送失败。')
  } finally {
    sending.value = false
  }
}

async function handleCancelAnswer(): Promise<void> {
  const conversation = selectedConversation.value
  const job = answerJob.value
  if (
    !conversation ||
    !job ||
    !canCancelAnswer.value ||
    canceling.value ||
    conversationMutationBusy.value
  )
    return

  const conversationId = conversation.id
  const selectionGeneration = conversationSelectionGeneration
  const jobId = job.id
  const operationIsCurrent = (): boolean =>
    conversationSelectionIsCurrent(conversationId, selectionGeneration) &&
    answerJob.value?.id === jobId
  canceling.value = true
  errorMessage.value = null
  try {
    const canceledJob = await cancelConversationAnswerJob(jobId)
    if (!operationIsCurrent()) return
    stopAnswerPolling()
    answerJob.value = canceledJob
    answerJobStatus.value = canceledJob.status
    await refreshLatestMessagesForSelection(conversationId, selectionGeneration)
    if (!operationIsCurrent()) return
    errorMessage.value = '回答任务已取消。'
  } catch (error) {
    if (!operationIsCurrent()) return
    const apiError = toApiError(error)
    if (apiError.kind === 'conflict') {
      try {
        const latestJob = await getConversationAnswerJob(jobId)
        if (!operationIsCurrent()) return
        answerJob.value = latestJob
        answerJobStatus.value = latestJob.status
        if (latestJob.status === 'queued' || latestJob.status === 'processing') {
          startAnswerPolling(latestJob, conversationId, selectionGeneration)
          errorMessage.value = '任务已经进入处理流程，暂时无法取消，系统将继续等待结果。'
        } else {
          await refreshLatestMessagesForSelection(conversationId, selectionGeneration)
          if (!operationIsCurrent()) return
          errorMessage.value =
            latestJob.status === 'canceled' ? '回答任务已取消。' : '回答任务已经结束。'
        }
      } catch (refreshError) {
        if (!operationIsCurrent()) return
        showRequestError(refreshError, '取消状态确认失败。')
      }
    } else {
      showRequestError(error, '回答任务取消失败。')
    }
  } finally {
    canceling.value = false
  }
}

async function handleCancelAgent(): Promise<void> {
  if (conversationArchived.value || conversationMutationBusy.value || !canCancelAgent.value) {
    return
  }
  await cancelAgent()
}

async function handleRestoreAgent(): Promise<void> {
  if (!selectedConversation.value) return
  await restoreAgentManually()
}

async function handleRetryAnswer(): Promise<void> {
  const conversation = selectedConversation.value
  const job = answerJob.value
  if (
    !conversation ||
    conversation.status !== 'active' ||
    !job ||
    !canRetryAnswer.value ||
    retrying.value ||
    conversationMutationBusy.value ||
    agentBusy.value ||
    agentActionBusy.value
  )
    return

  const conversationId = conversation.id
  const selectionGeneration = conversationSelectionGeneration
  const operationIsCurrent = (): boolean =>
    conversationSelectionIsCurrent(conversationId, selectionGeneration) &&
    answerJob.value?.id === job.id
  retrying.value = true
  errorMessage.value = null
  clearCapacityFailure()
  try {
    const result = await queueConversationAnswer(conversationId, {
      content: job.query,
      clientMessageId: crypto.randomUUID(),
      documentId: job.documentId ?? undefined,
      topK: job.topK,
      responseLanguage: job.requestedResponseLanguage,
    })
    if (!operationIsCurrent()) return
    mergeMessages([result.userMessage, result.assistantMessage])
    startAnswerPolling(result.answerJob, result.conversationId, selectionGeneration)
    await refreshConversationListForSelection(conversationId, selectionGeneration)
  } catch (error) {
    if (!operationIsCurrent()) return
    if (isExecutionConflict(error)) {
      await reconcileExecutionConflict(conversationId, selectionGeneration)
      return
    }
    showRequestError(error, '再次提交回答失败。')
  } finally {
    retrying.value = false
  }
}

function displaySourceTitle(source: GroundedAnswerSource): string {
  return source.title?.trim() || source.originalName
}

function sourcePageLabel(source: GroundedAnswerSource): string {
  if (source.pageStart === null) return '页码未知'
  if (source.pageEnd === null || source.pageStart === source.pageEnd) {
    return `第 ${source.pageStart} 页`
  }
  return `第 ${source.pageStart}–${source.pageEnd} 页`
}

function sourceSimilarityLabel(similarity: number): string {
  return `${Math.round(Math.max(0, Math.min(1, similarity)) * 100)}%`
}

onMounted(() => {
  void refreshConversations()
})

onBeforeUnmount(() => {
  conversationSelectionGeneration += 1
  stopAnswerPolling()
  clearAgentConversation()
  clearCapacityFailure()
})
</script>

<template>
  <div class="conversations-page">
    <PageHeader
      eyebrow="Conversations"
      title="对话记录"
      description="保存个人会话，并可选择保存消息、RAG 回答或受控 Agent Run。"
    />

    <div class="conversation-layout">
      <aside class="conversation-sidebar" aria-label="会话列表">
        <div class="sidebar-heading">
          <strong>我的会话</strong>
          <button type="button" :disabled="creating" @click="handleCreateConversation">
            {{ creating ? '创建中…' : '新建' }}
          </button>
        </div>

        <p v-if="loading" class="state-message">正在加载会话…</p>
        <p v-else-if="conversations.length === 0" class="state-message">还没有会话，先新建一个。</p>
        <div v-else class="conversation-list">
          <button
            v-for="conversation in conversations"
            :key="conversation.id"
            type="button"
            class="conversation-item"
            :class="{ 'conversation-item--active': selectedConversation?.id === conversation.id }"
            @click="selectConversation(conversation)"
          >
            <strong>{{ conversation.title }}</strong>
            <span>{{
              conversation.lastMessageAt ? conversation.lastMessageAt.toLocaleString() : '暂无消息'
            }}</span>
          </button>
          <button
            v-if="hasMoreConversations"
            type="button"
            class="load-more-button"
            :disabled="loadingMoreConversations"
            @click="loadMoreConversations"
          >
            {{ loadingMoreConversations ? '加载中…' : '加载更多会话' }}
          </button>
        </div>
      </aside>

      <section class="conversation-panel" aria-label="当前会话">
        <div v-if="!selectedConversation" class="empty-panel">
          <strong>选择一个会话开始</strong>
          <span>选择会话后，可以保存消息或显式提交回答任务。</span>
        </div>
        <template v-else>
          <div class="conversation-heading">
            <div class="conversation-title">
              <span class="conversation-kicker">当前会话</span>
              <h2>{{ selectedConversation.title }}</h2>
              <form
                v-if="editingTitle"
                class="rename-conversation-form"
                @submit.prevent="handleRenameConversation"
              >
                <input
                  v-model="titleDraft"
                  maxlength="200"
                  aria-label="新会话标题"
                  :disabled="renaming"
                />
                <button type="submit" :disabled="renaming || !titleDraft.trim()">
                  {{ renaming ? '保存中…' : '保存标题' }}
                </button>
                <button type="button" :disabled="renaming" @click="cancelRenameConversation">
                  取消
                </button>
              </form>
            </div>
            <div class="conversation-management">
              <span class="conversation-status">{{ selectedConversation.status }}</span>
              <button
                type="button"
                :disabled="
                  renaming ||
                  updatingStatus ||
                  deleting ||
                  sending ||
                  canceling ||
                  retrying ||
                  agentActionBusy
                "
                @click="beginRenameConversation"
              >
                重命名
              </button>
              <button
                v-if="selectedConversation.status === 'active'"
                type="button"
                :disabled="
                  renaming ||
                  updatingStatus ||
                  deleting ||
                  sending ||
                  canceling ||
                  retrying ||
                  agentActionBusy
                "
                @click="handleConversationStatus('archived')"
              >
                {{ updatingStatus ? '归档中…' : '归档' }}
              </button>
              <button
                v-else
                type="button"
                :disabled="
                  renaming ||
                  updatingStatus ||
                  deleting ||
                  sending ||
                  canceling ||
                  retrying ||
                  agentActionBusy
                "
                @click="handleConversationStatus('active')"
              >
                {{ updatingStatus ? '恢复中…' : '恢复' }}
              </button>
              <button
                type="button"
                class="danger-button"
                :disabled="
                  renaming ||
                  updatingStatus ||
                  deleting ||
                  sending ||
                  canceling ||
                  retrying ||
                  agentActionBusy
                "
                @click="requestDeleteConversation"
              >
                删除
              </button>
            </div>
          </div>

          <div
            v-if="deleteConfirmationConversationId === selectedConversation.id"
            class="delete-confirmation"
            role="alert"
          >
            <span>
              删除会话会永久删除消息、关联 Answer Job/Agent Run、来源与
              Token，且无法恢复。确认删除“{{ selectedConversation.title }}”？
            </span>
            <div>
              <button
                type="button"
                :disabled="deleting || sending || canceling || retrying || agentActionBusy"
                @click="handleDeleteConversation"
              >
                {{ deleting ? '删除中…' : '确认删除' }}
              </button>
              <button type="button" :disabled="deleting" @click="cancelDeleteConversation">
                取消
              </button>
            </div>
          </div>

          <p v-if="conversationArchived" class="archived-notice">
            此会话已归档。恢复后才能继续发送消息或提交新的回答执行。
          </p>

          <div class="message-list" aria-live="polite">
            <button
              v-if="hasEarlierMessages"
              type="button"
              class="load-more-button load-earlier-button"
              :disabled="loadingEarlierMessages"
              @click="loadEarlierMessages"
            >
              {{ loadingEarlierMessages ? '加载中…' : '加载更早消息' }}
            </button>
            <p v-if="messages.length === 0" class="state-message">
              还没有消息，发送第一条研究问题。
            </p>
            <article
              v-for="message in messages"
              :key="message.id"
              class="message-card"
              :class="`message-card--${message.role}`"
            >
              <span class="message-meta">
                <span class="message-role">{{ message.role === 'user' ? '我' : '助手' }}</span>
                <span v-if="message.status !== 'completed'" class="message-status">{{
                  message.status === 'generating'
                    ? '生成中'
                    : message.status === 'failed'
                      ? '失败'
                      : '已中断'
                }}</span>
              </span>
              <p>{{ message.content }}</p>
            </article>
          </div>

          <section
            v-if="answerJob?.status === 'succeeded' && answerJob.result"
            class="answer-evidence"
            aria-label="随会话保留的最近回答来源与用量"
          >
            <header>
              <strong>{{
                answerJob.result.sources.length === 0 && answerJob.result.usage.totalTokens === 0
                  ? '证据不足'
                  : '最近回答证据'
              }}</strong>
              <span>{{ answerJob.result.sources.length }} 条来源 · 随会话保留</span>
            </header>
            <dl class="answer-usage">
              <div>
                <dt>Prompt</dt>
                <dd>{{ answerJob.result.usage.promptTokens }}</dd>
              </div>
              <div>
                <dt>Completion</dt>
                <dd>{{ answerJob.result.usage.completionTokens }}</dd>
              </div>
              <div>
                <dt>Total</dt>
                <dd>{{ answerJob.result.usage.totalTokens }}</dd>
              </div>
            </dl>
            <ol v-if="answerJob.result.sources.length > 0" class="answer-sources">
              <li v-for="source in answerJob.result.sources" :key="source.chunkId">
                <strong>[{{ source.citation }}] {{ displaySourceTitle(source) }}</strong>
                <span>
                  {{ sourcePageLabel(source) }} · 文档 #{{ source.documentId }} · 文本块
                  {{ source.chunkIndex }} · 相似度 {{ sourceSimilarityLabel(source.similarity) }}
                </span>
              </li>
            </ol>
            <p v-else class="composer-hint">本次回答没有可展示的检索来源。</p>
          </section>

          <AgentRunPanel
            v-if="agentRun"
            :run="agentRun"
            :state="agentWorkspaceState"
            :error-message="agentErrorMessage"
            :request-id="agentRequestId"
            :read-only="conversationArchived"
            :manual-restore-required="agentManualRestoreRequired"
            @cancel="handleCancelAgent"
            @restore="handleRestoreAgent"
          />

          <form class="message-composer" @submit.prevent="handleSendMessage">
            <DocumentScopePicker
              v-if="composerMode === 'answer' || composerMode === 'agent'"
              v-model="documentScope"
              :disabled="
                conversationArchived ||
                conversationMutationBusy ||
                sending ||
                executionBusy ||
                composerCoolingDown
              "
            />
            <div
              v-if="composerMode === 'answer' || composerMode === 'agent'"
              class="answer-options"
            >
              <label>
                <span>回答语言</span>
                <select
                  v-model="responseLanguage"
                  :disabled="
                    conversationArchived ||
                    conversationMutationBusy ||
                    sending ||
                    executionBusy ||
                    composerCoolingDown
                  "
                >
                  <option value="auto">跟随问题</option>
                  <option value="zh">中文</option>
                  <option value="en">English</option>
                </select>
              </label>
              <label v-if="composerMode === 'answer'">
                <span>最多引用</span>
                <select
                  v-model.number="topK"
                  :disabled="
                    conversationArchived ||
                    conversationMutationBusy ||
                    sending ||
                    executionBusy ||
                    composerCoolingDown
                  "
                >
                  <option v-for="value in 20" :key="value" :value="value">{{ value }} 条</option>
                </select>
              </label>
            </div>
            <textarea
              v-model="draft"
              aria-label="消息内容"
              placeholder="输入一条消息…"
              :disabled="
                conversationArchived || conversationMutationBusy || sending || executionBusy
              "
              @keydown.enter.exact.prevent="handleSendMessage"
            />
            <fieldset class="composer-modes" aria-label="发送模式">
              <label>
                <input
                  v-model="composerMode"
                  type="radio"
                  name="composer-mode"
                  value="message"
                  :disabled="conversationArchived || sending || executionBusy || agentActionBusy"
                />
                <span>仅保存消息</span>
              </label>
              <label>
                <input
                  v-model="composerMode"
                  type="radio"
                  name="composer-mode"
                  value="answer"
                  :disabled="conversationArchived || sending || executionBusy || agentActionBusy"
                />
                <span>RAG 回答</span>
              </label>
              <label v-if="agentAvailability !== 'unavailable'">
                <input
                  v-model="composerMode"
                  type="radio"
                  name="composer-mode"
                  value="agent"
                  :disabled="
                    conversationArchived ||
                    sending ||
                    executionBusy ||
                    agentActionBusy ||
                    agentAvailability !== 'available'
                  "
                />
                <span>Agent</span>
              </label>
            </fieldset>
            <div class="composer-footer">
              <span v-if="answerJobStatus" class="answer-job-status">
                {{
                  answerJobStatus === 'queued'
                    ? '回答排队中…'
                    : answerJobStatus === 'processing'
                      ? '回答生成中…'
                      : answerJobStatus === 'succeeded'
                        ? '回答已完成'
                        : '回答未完成'
                }}
              </span>
              <span v-else class="composer-hint">{{
                composerMode === 'answer'
                  ? '需要后端开启 Answer Job。'
                  : composerMode === 'agent'
                    ? 'Agent 使用受控只读工具并通过轮询恢复。'
                    : '当前只保存消息。'
              }}</span>
              <button
                type="submit"
                :disabled="
                  conversationArchived ||
                  conversationMutationBusy ||
                  sending ||
                  !draft.trim() ||
                  executionBusy ||
                  composerCoolingDown ||
                  (composerMode === 'agent' && agentAvailability !== 'available')
                "
              >
                {{
                  sending
                    ? composerMode === 'message'
                      ? '保存中…'
                      : '提交中…'
                    : composerMode === 'answer'
                      ? '提交 RAG 回答'
                      : composerMode === 'agent'
                        ? '提交 Agent Run'
                        : '发送'
                }}
              </button>
            </div>
            <div v-if="answerJob" class="answer-job-actions">
              <button
                v-if="canCancelAnswer"
                type="button"
                class="secondary-button"
                :disabled="conversationArchived || conversationMutationBusy || canceling"
                @click="handleCancelAnswer"
              >
                {{ canceling ? '取消中…' : '取消回答' }}
              </button>
              <button
                v-if="canRetryAnswer"
                type="button"
                class="secondary-button"
                :disabled="
                  conversationArchived ||
                  conversationMutationBusy ||
                  retrying ||
                  capacityRetryRemaining > 0 ||
                  agentBusy ||
                  agentActionBusy
                "
                @click="handleRetryAnswer"
              >
                {{ retrying ? '再次提交中…' : '再次提交此问题' }}
              </button>
              <span v-if="answerJobStatus === 'processing'" class="composer-hint">
                Worker 已开始处理，当前不能取消。
              </span>
            </div>
          </form>
        </template>
      </section>
    </div>

    <p
      v-if="composerMode === 'agent' && agentCapacityFailure"
      class="capacity-message"
      role="alert"
    >
      <strong>{{ agentCapacityFailure.title }}</strong
      >：{{ agentCapacityFailure.message }}
      <span v-if="agentRetryRemaining > 0">{{ agentRetryRemaining }} 秒后可再次提交。</span>
      <span v-else>现在可以再次提交。</span>
      <span v-if="agentCapacityFailure.requestId"
        >请求编号：{{ agentCapacityFailure.requestId }}</span
      >
    </p>
    <p v-else-if="capacityFailure" class="capacity-message" role="alert">
      <strong>{{ capacityFailure.title }}</strong
      >：{{ capacityFailure.message }}
      <span v-if="capacityRetryRemaining > 0">{{ capacityRetryRemaining }} 秒后可再次提交。</span>
      <span v-else>现在可以再次提交。</span>
      <span v-if="capacityFailure.requestId">请求编号：{{ capacityFailure.requestId }}</span>
    </p>
    <p
      v-else-if="composerMode === 'agent' && agentErrorMessage && !agentRun"
      class="error-message"
      role="alert"
    >
      {{ agentErrorMessage }}
      <span v-if="agentRequestId">请求编号：{{ agentRequestId }}</span>
    </p>
    <p v-else-if="errorMessage" class="error-message" role="alert">
      {{ errorMessage }}
      <button
        v-if="executionConflictUnresolved"
        type="button"
        class="execution-reconcile-button"
        :disabled="agentActionBusy"
        @click="handleReconcileExecution"
      >
        重新检查执行状态
      </button>
    </p>
  </div>
</template>

<style scoped>
.conversations-page {
  display: grid;
  gap: 24px;
  max-width: 1180px;
  margin: 0 auto;
}

.conversation-layout {
  display: grid;
  min-height: 620px;
  grid-template-columns: minmax(220px, 280px) minmax(0, 1fr);
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: 18px;
  background: var(--color-surface);
}

.conversation-sidebar {
  padding: 18px;
  border-right: 1px solid var(--color-border);
  background: var(--color-surface-subtle);
}

.sidebar-heading,
.conversation-heading,
.composer-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.sidebar-heading button,
.composer-footer button {
  padding: 8px 12px;
  border: 1px solid var(--color-border-strong);
  border-radius: 8px;
  background: var(--color-text-strong);
  color: var(--color-surface);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
}

.sidebar-heading button:disabled,
.composer-footer button:disabled {
  cursor: wait;
  opacity: 0.55;
}

.answer-job-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 10px;
}

.secondary-button {
  padding: 7px 11px;
  border: 1px solid var(--color-border-strong);
  border-radius: 8px;
  background: var(--color-surface);
  color: var(--color-text-strong);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
}

.secondary-button:disabled {
  cursor: wait;
  opacity: 0.55;
}

.capacity-message {
  display: grid;
  gap: 4px;
  margin: 0;
  padding: 12px 14px;
  border: 1px solid var(--color-highlight);
  border-radius: 10px;
  background: #fff9db;
  color: var(--color-text-strong);
}

.conversation-list {
  display: grid;
  gap: 6px;
  margin-top: 18px;
}

.conversation-item {
  display: grid;
  gap: 5px;
  width: 100%;
  padding: 11px;
  border: 1px solid transparent;
  border-radius: 10px;
  background: transparent;
  color: var(--color-text-strong);
  cursor: pointer;
  text-align: left;
}

.conversation-item:hover,
.conversation-item--active {
  border-color: var(--color-border);
  background: var(--color-surface);
}

.conversation-item span,
.conversation-kicker,
.conversation-status,
.composer-footer span,
.state-message,
.empty-panel span {
  color: var(--color-text-muted);
  font-size: 12px;
}

.conversation-panel {
  display: grid;
  min-width: 0;
  grid-template-rows: auto minmax(0, 1fr) auto auto;
}

.load-more-button {
  width: 100%;
  padding: 9px 10px;
  border: 1px dashed var(--color-border-strong);
  border-radius: 9px;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
}

.load-more-button:disabled {
  cursor: wait;
  opacity: 0.55;
}

.load-earlier-button {
  max-width: 220px;
  justify-self: center;
}

.conversation-heading {
  align-items: flex-start;
  padding: 20px 24px;
  border-bottom: 1px solid var(--color-border);
}

.conversation-title {
  min-width: 0;
}

.conversation-heading h2 {
  margin: 5px 0 0;
  font-size: 18px;
}

.conversation-management {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 7px;
}

.conversation-management button,
.rename-conversation-form button,
.delete-confirmation button {
  padding: 6px 9px;
  border: 1px solid var(--color-border-strong);
  border-radius: 7px;
  background: var(--color-surface);
  color: var(--color-text-strong);
  cursor: pointer;
  font: inherit;
  font-size: 11px;
}

.conversation-management button:disabled,
.rename-conversation-form button:disabled,
.delete-confirmation button:disabled {
  cursor: wait;
  opacity: 0.55;
}

.conversation-management .danger-button,
.delete-confirmation button:first-child {
  border-color: var(--color-danger);
  color: var(--color-danger);
}

.rename-conversation-form {
  display: flex;
  flex-wrap: wrap;
  gap: 7px;
  margin-top: 10px;
}

.rename-conversation-form input {
  min-width: min(320px, 70vw);
  padding: 7px 9px;
  border: 1px solid var(--color-border-strong);
  border-radius: 7px;
  background: var(--color-surface);
  color: var(--color-text-strong);
  font: inherit;
  font-size: 12px;
}

.delete-confirmation,
.archived-notice {
  margin: 0;
  padding: 12px 24px;
  border-bottom: 1px solid var(--color-border);
  font-size: 12px;
}

.delete-confirmation {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  background: var(--color-danger-soft);
}

.delete-confirmation > div {
  display: flex;
  flex: 0 0 auto;
  gap: 7px;
}

.archived-notice {
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
}

.conversation-status {
  padding: 4px 8px;
  border-radius: 999px;
  background: var(--color-surface-subtle);
}

.message-list {
  display: grid;
  align-content: start;
  gap: 12px;
  padding: 24px;
  overflow: auto;
}

.message-card {
  max-width: 78%;
  padding: 12px 14px;
  border: 1px solid var(--color-border);
  border-radius: 12px;
}

.message-card--user {
  justify-self: end;
  background: var(--color-surface-active);
}

.message-card--assistant {
  justify-self: start;
  background: var(--color-surface-subtle);
}

.message-meta {
  display: flex;
  align-items: center;
  gap: 8px;
}

.message-role {
  color: var(--color-text-muted);
  font-size: 11px;
  font-weight: 600;
}

.message-status,
.answer-job-status {
  color: var(--color-text-muted);
  font-size: 11px;
}

.message-card p {
  margin: 7px 0 0;
  white-space: pre-wrap;
  line-height: 1.6;
}

.answer-evidence {
  display: grid;
  gap: 12px;
  padding: 16px 24px;
  border-top: 1px solid var(--color-border);
  background: var(--color-surface-subtle);
}

.answer-evidence > header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.answer-evidence > header span {
  color: var(--color-text-muted);
  font-size: 11px;
}

.answer-usage {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin: 0;
}

.answer-usage div {
  padding: 8px 10px;
  border: 1px solid var(--color-border);
  border-radius: 8px;
  background: var(--color-surface);
}

.answer-usage dt,
.answer-usage dd {
  margin: 0;
}

.answer-usage dt {
  color: var(--color-text-muted);
  font-size: 10px;
}

.answer-usage dd {
  margin-top: 3px;
  font-weight: 700;
}

.answer-sources {
  display: grid;
  gap: 8px;
  max-height: 180px;
  margin: 0;
  padding-left: 20px;
  overflow: auto;
}

.answer-sources li {
  padding-left: 4px;
}

.answer-sources strong,
.answer-sources span {
  display: block;
}

.answer-sources span {
  margin-top: 3px;
  color: var(--color-text-muted);
  font-size: 11px;
}

.message-composer {
  display: grid;
  gap: 10px;
  padding: 18px 24px;
  border-top: 1px solid var(--color-border);
}

.message-composer textarea {
  min-height: 76px;
  resize: vertical;
  padding: 11px 12px;
  border: 1px solid var(--color-border-strong);
  border-radius: 10px;
  background: var(--color-surface);
  color: var(--color-text-strong);
  font: inherit;
}

.answer-options {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.answer-options label {
  display: grid;
  gap: 6px;
  color: var(--color-text-strong);
  font-size: 12px;
  font-weight: 650;
}

.answer-options select {
  height: 38px;
  padding: 0 10px;
  border: 1px solid var(--color-border-strong);
  border-radius: 8px;
  background: var(--color-surface);
  color: var(--color-text-strong);
}

.composer-modes {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 0;
  padding: 0;
  border: 0;
}

.composer-modes label {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 7px 10px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  color: var(--color-text-strong);
  font-size: 12px;
}

.composer-modes input {
  accent-color: var(--color-text-strong);
}

.composer-hint {
  color: var(--color-text-muted);
  font-size: 12px;
}

.empty-panel {
  display: grid;
  align-content: center;
  justify-items: center;
  gap: 8px;
  min-height: 400px;
  padding: 24px;
  text-align: center;
}

.error-message {
  margin: 0;
  color: var(--color-danger);
  font-size: 13px;
}

.execution-reconcile-button {
  margin-left: 8px;
}

@media (max-width: 760px) {
  .conversation-layout {
    grid-template-columns: 1fr;
  }

  .conversation-sidebar {
    border-right: 0;
    border-bottom: 1px solid var(--color-border);
  }

  .conversation-list {
    display: flex;
    overflow-x: auto;
  }

  .conversation-item {
    min-width: 210px;
  }

  .message-card {
    max-width: 92%;
  }
}
</style>
