import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type {
  Conversation,
  ConversationAnswerJob,
  ConversationMessage,
} from '../entities/conversation/model/conversation'
import type { AgentRun } from '../entities/agent/model/agent-run'
import { ApiError } from '../shared/api/api-error'
import {
  cancelAgentRun,
  createAgentRun,
  getActiveAgentRun,
  getAgentRun,
  getLatestAgentRun,
} from '../features/agents/api/agent-api'
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
import ConversationsPage from './ConversationsPage.vue'

vi.mock('../features/conversations/api/conversation-api', () => ({
  appendUserMessage: vi.fn(),
  cancelConversationAnswerJob: vi.fn(),
  createConversation: vi.fn(),
  deleteConversation: vi.fn(),
  getActiveConversationAnswerJob: vi.fn(),
  getConversationAnswerJob: vi.fn(),
  getLatestConversationAnswerJob: vi.fn(),
  listConversationMessages: vi.fn(),
  listConversationMessagesCursor: vi.fn(),
  listConversations: vi.fn(),
  listConversationsCursor: vi.fn(),
  queueConversationAnswer: vi.fn(),
  updateConversation: vi.fn(),
}))

vi.mock('../features/agents/api/agent-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../features/agents/api/agent-api')>()
  return {
    ...actual,
    cancelAgentRun: vi.fn(),
    createAgentRun: vi.fn(),
    getActiveAgentRun: vi.fn(),
    getAgentRun: vi.fn(),
    getLatestAgentRun: vi.fn(),
  }
})

vi.mock('../features/documents/model/use-document-scope-options', async () => {
  const { computed, ref, shallowRef } = await import('vue')
  const documents = shallowRef([
    {
      id: 7,
      title: '研究文档',
      originalName: 'study.pdf',
      mimeType: 'application/pdf',
      sizeBytes: 1024,
      sha256: 'a'.repeat(64),
      status: 'ready',
      errorMessage: null,
      createdAt: new Date('2026-10-31T00:00:00Z'),
      updatedAt: new Date('2026-10-31T00:00:00Z'),
    },
  ])
  const state = ref('success')
  return {
    useDocumentScopeOptions: () => ({
      documents,
      errorMessage: ref(''),
      isLoading: computed(() => false),
      load: vi.fn(),
      requestId: ref(undefined),
      state,
    }),
  }
})

const appendUserMessageMock = vi.mocked(appendUserMessage)
const cancelAgentRunMock = vi.mocked(cancelAgentRun)
const createAgentRunMock = vi.mocked(createAgentRun)
const getActiveAgentRunMock = vi.mocked(getActiveAgentRun)
const getAgentRunMock = vi.mocked(getAgentRun)
const getLatestAgentRunMock = vi.mocked(getLatestAgentRun)
const cancelConversationAnswerJobMock = vi.mocked(cancelConversationAnswerJob)
const createConversationMock = vi.mocked(createConversation)
const deleteConversationMock = vi.mocked(deleteConversation)
const getActiveConversationAnswerJobMock = vi.mocked(getActiveConversationAnswerJob)
const getConversationAnswerJobMock = vi.mocked(getConversationAnswerJob)
const getLatestConversationAnswerJobMock = vi.mocked(getLatestConversationAnswerJob)
const listConversationMessagesMock = vi.mocked(listConversationMessages)
const listConversationMessagesCursorMock = vi.mocked(listConversationMessagesCursor)
const listConversationsMock = vi.mocked(listConversations)
const listConversationsCursorMock = vi.mocked(listConversationsCursor)
const queueConversationAnswerMock = vi.mocked(queueConversationAnswer)
const updateConversationMock = vi.mocked(updateConversation)

const conversation: Conversation = {
  id: 11,
  title: '研究问题',
  status: 'active',
  lastMessageAt: null,
  createdAt: new Date('2026-10-31T01:00:00Z'),
  updatedAt: new Date('2026-10-31T01:00:00Z'),
}

const userMessage: ConversationMessage = {
  id: 20,
  conversationId: 11,
  sequenceNo: 1,
  role: 'user',
  content: '解释这份文档',
  status: 'completed',
  clientMessageId: 'client-1',
  createdAt: new Date('2026-10-31T01:01:00Z'),
  updatedAt: new Date('2026-10-31T01:01:00Z'),
}

const assistantGenerating: ConversationMessage = {
  ...userMessage,
  id: 21,
  sequenceNo: 2,
  role: 'assistant',
  content: '正在生成回答…',
  status: 'generating',
  clientMessageId: null,
}

const assistantCanceled: ConversationMessage = {
  ...assistantGenerating,
  content: '回答已取消。',
  status: 'interrupted',
  updatedAt: new Date('2026-10-31T01:02:00Z'),
}

const queuedJob: ConversationAnswerJob = {
  id: 31,
  documentId: null,
  conversationId: 11,
  userMessageId: 20,
  assistantMessageId: 21,
  query: '解释这份文档',
  topK: 5,
  requestedResponseLanguage: 'auto',
  status: 'queued',
  cancelable: true,
  attemptCount: 0,
  errorCode: null,
  errorMessage: null,
  result: null,
  nextAttemptAt: new Date('2026-10-31T01:01:00Z'),
  createdAt: new Date('2026-10-31T01:01:00Z'),
  updatedAt: new Date('2026-10-31T01:01:00Z'),
  startedAt: null,
  completedAt: null,
}

const canceledJob: ConversationAnswerJob = {
  ...queuedJob,
  status: 'canceled',
  cancelable: false,
  updatedAt: new Date('2026-10-31T01:02:00Z'),
  completedAt: new Date('2026-10-31T01:02:00Z'),
}

const failedJob: ConversationAnswerJob = {
  ...queuedJob,
  documentId: 7,
  query: '解释这份文档',
  topK: 8,
  requestedResponseLanguage: 'zh',
  status: 'failed',
  cancelable: false,
  errorCode: 'answer_execution_failed',
  errorMessage: '暂时无法生成回答，请稍后重试。',
  updatedAt: new Date('2026-10-31T01:02:00Z'),
  completedAt: new Date('2026-10-31T01:02:00Z'),
}

const succeededJob: ConversationAnswerJob = {
  ...queuedJob,
  documentId: 7,
  topK: 8,
  requestedResponseLanguage: 'zh',
  status: 'succeeded',
  cancelable: false,
  result: {
    answer: '系统通过证据回答。[1]',
    responseLanguage: 'zh',
    sources: [
      {
        citation: 1,
        chunkId: 501,
        documentId: 7,
        chunkIndex: 2,
        title: '研究文档',
        originalName: 'study.pdf',
        pageStart: 3,
        pageEnd: 4,
        similarity: 0.88,
      },
    ],
    usage: { promptTokens: 100, completionTokens: 20, totalTokens: 120 },
  },
  completedAt: new Date('2026-10-31T01:02:00Z'),
}

const queuedAgentRun: AgentRun = {
  id: 71,
  conversationId: 11,
  userMessageId: 20,
  assistantMessageId: 21,
  clientRunId: 'agent-run-71',
  requestedDocumentId: null,
  requestedResponseLanguage: 'auto',
  status: 'queued',
  stepCount: 0,
  maxSteps: 5,
  maxTotalTokens: 48_000,
  cancelable: true,
  steps: [],
  result: null,
  error: null,
  tokenUsage: { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
  createdAt: new Date('2026-10-31T01:01:00Z'),
  updatedAt: new Date('2026-10-31T01:01:00Z'),
  startedAt: null,
  completedAt: null,
}

const processingAgentRun: AgentRun = {
  ...queuedAgentRun,
  status: 'processing',
  cancelable: false,
  startedAt: new Date('2026-10-31T01:01:10Z'),
}

const succeededAgentRun: AgentRun = {
  ...queuedAgentRun,
  status: 'succeeded',
  stepCount: 3,
  cancelable: false,
  steps: [
    {
      id: 81,
      agentRunId: 71,
      sequenceNo: 1,
      kind: 'tool_call',
      status: 'succeeded',
      toolName: 'search_documents',
      error: null,
      tokenUsage: { promptTokens: 4, completionTokens: 1, totalTokens: 5 },
      createdAt: new Date('2026-10-31T01:01:10Z'),
      startedAt: new Date('2026-10-31T01:01:10Z'),
      completedAt: new Date('2026-10-31T01:01:12Z'),
    },
    {
      id: 82,
      agentRunId: 71,
      sequenceNo: 2,
      kind: 'tool_call',
      status: 'succeeded',
      toolName: 'read_document_evidence',
      error: null,
      tokenUsage: { promptTokens: 3, completionTokens: 1, totalTokens: 4 },
      createdAt: new Date('2026-10-31T01:01:13Z'),
      startedAt: new Date('2026-10-31T01:01:13Z'),
      completedAt: new Date('2026-10-31T01:01:15Z'),
    },
    {
      id: 83,
      agentRunId: 71,
      sequenceNo: 3,
      kind: 'final',
      status: 'succeeded',
      toolName: null,
      error: null,
      tokenUsage: { promptTokens: 3, completionTokens: 3, totalTokens: 6 },
      createdAt: new Date('2026-10-31T01:01:16Z'),
      startedAt: new Date('2026-10-31T01:01:16Z'),
      completedAt: new Date('2026-10-31T01:01:20Z'),
    },
  ],
  result: {
    answer: 'Agent 最终回答。[1]',
    responseLanguage: 'zh',
    sources: [
      {
        citation: 1,
        evidenceRef: 'step-1:item-1',
        documentId: 7,
        chunkId: 501,
        title: '研究文档',
        pageStart: 3,
        pageEnd: 4,
        contentHash: 'a'.repeat(64),
      },
    ],
  },
  tokenUsage: { promptTokens: 10, completionTokens: 5, totalTokens: 15 },
  startedAt: new Date('2026-10-31T01:01:10Z'),
  completedAt: new Date('2026-10-31T01:01:20Z'),
}

const assistantFailed: ConversationMessage = {
  ...assistantGenerating,
  content: '暂时无法生成回答，请稍后重试。',
  status: 'failed',
  updatedAt: new Date('2026-10-31T01:02:00Z'),
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolver) => {
    resolve = resolver
  })
  return { promise, resolve }
}

async function selectComposerMode(
  wrapper: ReturnType<typeof mount>,
  mode: 'message' | 'answer' | 'agent',
): Promise<void> {
  await wrapper.get(`input[name="composer-mode"][value="${mode}"]`).setValue(true)
}

beforeEach(() => {
  cancelAgentRunMock.mockReset()
  createAgentRunMock.mockReset()
  getActiveAgentRunMock.mockReset()
  getAgentRunMock.mockReset()
  getLatestAgentRunMock.mockReset()
  appendUserMessageMock.mockReset()
  cancelConversationAnswerJobMock.mockReset()
  createConversationMock.mockReset()
  deleteConversationMock.mockReset()
  getActiveConversationAnswerJobMock.mockReset()
  getConversationAnswerJobMock.mockReset()
  getLatestConversationAnswerJobMock.mockReset()
  listConversationMessagesMock.mockReset()
  listConversationMessagesCursorMock.mockReset()
  listConversationsMock.mockReset()
  listConversationsCursorMock.mockReset()
  queueConversationAnswerMock.mockReset()
  updateConversationMock.mockReset()
  listConversationsMock.mockResolvedValue({
    conversations: [conversation],
    pagination: { page: 1, pageSize: 20, total: 1, totalPages: 1 },
  })
  listConversationMessagesMock.mockResolvedValue({
    conversationId: 11,
    messages: [],
    pagination: { page: 1, pageSize: 50, total: 0, totalPages: 0 },
  })
  listConversationsCursorMock.mockImplementation(async ({ cursor }) => {
    const page = cursor ? 2 : 1
    const result = await listConversationsMock({ page, pageSize: 20 })
    const hasMore = result.pagination.page < result.pagination.totalPages
    return {
      conversations: result.conversations,
      cursor: {
        next: hasMore ? 'conversation-cursor-' + (page + 1) : null,
        hasMore,
      },
    }
  })
  listConversationMessagesCursorMock.mockImplementation(async (conversationId, { cursor }) => {
    const page = cursor ? 2 : 1
    const result = await listConversationMessagesMock(conversationId, {
      page,
      pageSize: 50,
      order: 'desc',
    })
    const hasMore = result.pagination.page < result.pagination.totalPages
    return {
      conversationId: result.conversationId,
      messages: [...result.messages].sort((left, right) => left.sequenceNo - right.sequenceNo),
      cursor: {
        next: hasMore ? 'message-cursor-' + (page + 1) : null,
        hasMore,
      },
    }
  })
  getActiveConversationAnswerJobMock.mockResolvedValue(null)
  getLatestConversationAnswerJobMock.mockResolvedValue(null)
  getActiveAgentRunMock.mockResolvedValue(null)
  getLatestAgentRunMock.mockResolvedValue(null)
})

afterEach(() => vi.useRealTimers())

describe('ConversationsPage', () => {
  it('reads messages before latest so a newly visible generating turn resumes polling', async () => {
    const messageResponse = deferred<Awaited<ReturnType<typeof listConversationMessages>>>()
    listConversationMessagesMock.mockReturnValueOnce(messageResponse.promise)
    getLatestConversationAnswerJobMock.mockResolvedValue(queuedJob)
    getConversationAnswerJobMock.mockResolvedValue(queuedJob)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    expect(getLatestConversationAnswerJobMock).not.toHaveBeenCalled()

    messageResponse.resolve({
      conversationId: 11,
      messages: [userMessage, assistantGenerating],
      pagination: { page: 1, pageSize: 50, total: 2, totalPages: 1 },
    })
    await flushPromises()

    expect(getLatestConversationAnswerJobMock).toHaveBeenCalledWith(11)
    expect(wrapper.text()).toContain('回答排队中')
    expect(wrapper.get('.secondary-button').text()).toContain('取消回答')
    wrapper.unmount()
  })

  it('loads more conversations with ID deduplication and stable newest-first order', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      lastMessageAt: new Date('2026-10-31T00:30:00Z'),
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const thirdConversation: Conversation = {
      ...conversation,
      id: 13,
      title: '第三个会话',
      lastMessageAt: new Date('2026-10-31T00:15:00Z'),
      updatedAt: new Date('2026-10-31T00:15:00Z'),
    }
    listConversationsMock
      .mockResolvedValueOnce({
        conversations: [conversation, secondConversation],
        pagination: { page: 1, pageSize: 20, total: 21, totalPages: 2 },
      })
      .mockResolvedValueOnce({
        conversations: [secondConversation, thirdConversation],
        pagination: { page: 2, pageSize: 20, total: 21, totalPages: 2 },
      })
      .mockResolvedValueOnce({
        conversations: [
          {
            ...conversation,
            lastMessageAt: new Date('2026-10-31T01:30:00Z'),
            updatedAt: new Date('2026-10-31T01:30:00Z'),
          },
        ],
        pagination: { page: 1, pageSize: 20, total: 21, totalPages: 2 },
      })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper.get('.conversation-sidebar .load-more-button').trigger('click')
    await flushPromises()

    const items = wrapper.findAll('.conversation-item')
    expect(items).toHaveLength(3)
    expect(items.map((item) => item.text())).toEqual([
      expect.stringContaining('研究问题'),
      expect.stringContaining('第二个会话'),
      expect.stringContaining('第三个会话'),
    ])
    expect(listConversationsCursorMock).toHaveBeenNthCalledWith(2, {
      pageSize: 20,
      cursor: 'conversation-cursor-2',
    })

    appendUserMessageMock.mockResolvedValue({ created: true, message: userMessage })
    await wrapper.get('textarea').setValue('保存一条新消息')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.findAll('.conversation-item')).toHaveLength(3)
    expect(wrapper.find('.conversation-sidebar .load-more-button').exists()).toBe(false)
    wrapper.unmount()
  })

  it('loads descending message pages, deduplicates overlap, and displays ascending sequence', async () => {
    const first = { ...userMessage, id: 1, sequenceNo: 1, content: '消息 1' }
    const second = {
      ...assistantGenerating,
      id: 2,
      sequenceNo: 2,
      content: '消息 2',
      status: 'completed' as const,
    }
    const third = { ...userMessage, id: 3, sequenceNo: 3, content: '消息 3' }
    const fourth = {
      ...assistantGenerating,
      id: 4,
      sequenceNo: 4,
      content: '消息 4',
      status: 'completed' as const,
    }
    listConversationMessagesMock
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [fourth, third],
        pagination: { page: 1, pageSize: 50, total: 51, totalPages: 2 },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [third, second, first],
        pagination: { page: 2, pageSize: 50, total: 51, totalPages: 2 },
      })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    expect(wrapper.findAll('.message-card').map((item) => item.text())).toEqual([
      expect.stringContaining('消息 3'),
      expect.stringContaining('消息 4'),
    ])

    await wrapper.get('.load-earlier-button').trigger('click')
    await flushPromises()

    expect(wrapper.findAll('.message-card').map((item) => item.text())).toEqual([
      expect.stringContaining('消息 1'),
      expect.stringContaining('消息 2'),
      expect.stringContaining('消息 3'),
      expect.stringContaining('消息 4'),
    ])
    expect(listConversationMessagesCursorMock).toHaveBeenNthCalledWith(2, 11, {
      pageSize: 50,
      cursor: 'message-cursor-2',
    })
    wrapper.unmount()
  })

  it('keeps the oldest message cursor after a terminal head refresh', async () => {
    const first = { ...userMessage, id: 1, sequenceNo: 1, content: '消息 1' }
    const second = {
      ...assistantGenerating,
      id: 2,
      sequenceNo: 2,
      content: '消息 2',
      status: 'completed' as const,
    }
    const third = { ...userMessage, id: 3, sequenceNo: 3, content: '消息 3' }
    const fourth = {
      ...assistantGenerating,
      id: 4,
      sequenceNo: 4,
      content: '消息 4',
      status: 'completed' as const,
    }
    const queued = {
      ...queuedJob,
      id: 32,
      userMessageId: 30,
      assistantMessageId: 31,
    }
    const succeeded = {
      ...succeededJob,
      id: 32,
      userMessageId: 30,
      assistantMessageId: 31,
    }
    listConversationMessagesCursorMock
      .mockReset()
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [third, fourth],
        cursor: { next: 'older-1', hasMore: true },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [first, second],
        cursor: { next: 'older-2', hasMore: true },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [
          { ...userMessage, id: 30, sequenceNo: 5 },
          {
            ...assistantGenerating,
            id: 31,
            sequenceNo: 6,
            status: 'completed',
            content: '最终回答',
          },
        ],
        cursor: { next: 'new-head-cursor', hasMore: true },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        cursor: { next: null, hasMore: false },
      })
    queueConversationAnswerMock.mockResolvedValue({
      conversationId: 11,
      userMessage: { ...userMessage, id: 30, sequenceNo: 5 },
      assistantMessage: { ...assistantGenerating, id: 31, sequenceNo: 6 },
      answerJob: queued,
    })
    getConversationAnswerJobMock.mockResolvedValue(succeeded)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper.get('.load-earlier-button').trigger('click')
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('新问题')
    await wrapper.get('.message-composer').trigger('submit')
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.load-earlier-button').exists()).toBe(true)
    await wrapper.get('.load-earlier-button').trigger('click')
    await flushPromises()
    expect(listConversationMessagesCursorMock).toHaveBeenNthCalledWith(4, 11, {
      pageSize: 50,
      cursor: 'older-2',
    })
    wrapper.unmount()
  })

  it('advances the head cursor to bridge a sequence gap, then deduplicates into the old window', async () => {
    const messagesForRange = (start: number, end: number): ConversationMessage[] =>
      Array.from({ length: end - start + 1 }, (_, index) => {
        const sequenceNo = start + index
        return {
          ...userMessage,
          id: 1000 + sequenceNo,
          sequenceNo,
          content: '消息 ' + sequenceNo,
        }
      })
    const latePoll = deferred<ConversationAnswerJob>()
    listConversationMessagesCursorMock
      .mockReset()
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: messagesForRange(51, 100),
        cursor: { next: 'before51', hasMore: true },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: messagesForRange(151, 200),
        cursor: { next: 'before151', hasMore: true },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: messagesForRange(101, 150),
        cursor: { next: 'before101', hasMore: true },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: messagesForRange(51, 100),
        cursor: { next: 'before51', hasMore: true },
      })
    getLatestConversationAnswerJobMock.mockResolvedValue(queuedJob)
    getConversationAnswerJobMock.mockReturnValue(latePoll.promise)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    expect(wrapper.findAll('.message-card')).toHaveLength(50)

    latePoll.resolve(succeededJob)
    await flushPromises()
    await flushPromises()
    expect(wrapper.findAll('.message-card')).toHaveLength(100)

    await wrapper.get('.load-earlier-button').trigger('click')
    await flushPromises()
    expect(listConversationMessagesCursorMock).toHaveBeenNthCalledWith(3, 11, {
      pageSize: 50,
      cursor: 'before151',
    })
    expect(wrapper.findAll('.message-card')).toHaveLength(150)
    expect(wrapper.text()).toContain('消息 101')
    expect(wrapper.text()).toContain('消息 150')

    await wrapper.get('.load-earlier-button').trigger('click')
    await flushPromises()
    expect(listConversationMessagesCursorMock).toHaveBeenNthCalledWith(4, 11, {
      pageSize: 50,
      cursor: 'before101',
    })
    expect(wrapper.findAll('.message-card')).toHaveLength(150)
    wrapper.unmount()
  })

  it('falls back each list instance to legacy paging when its cursor route is unavailable', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    listConversationsCursorMock.mockRejectedValue(
      new ApiError('not-found', 'cursor route unavailable', { status: 404 }),
    )
    listConversationMessagesCursorMock.mockRejectedValue(
      new ApiError('not-found', 'cursor route unavailable', { status: 404 }),
    )
    listConversationsMock
      .mockResolvedValueOnce({
        conversations: [conversation],
        pagination: { page: 1, pageSize: 20, total: 21, totalPages: 2 },
      })
      .mockResolvedValueOnce({
        conversations: [secondConversation],
        pagination: { page: 2, pageSize: 20, total: 21, totalPages: 2 },
      })
    listConversationMessagesMock
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [userMessage],
        pagination: { page: 1, pageSize: 50, total: 51, totalPages: 2 },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 2, pageSize: 50, total: 51, totalPages: 2 },
      })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper.get('.conversation-sidebar .load-more-button').trigger('click')
    await wrapper.get('.load-earlier-button').trigger('click')
    await flushPromises()

    expect(listConversationsCursorMock).toHaveBeenCalledTimes(1)
    expect(listConversationMessagesCursorMock).toHaveBeenCalledTimes(1)
    expect(listConversationsMock).toHaveBeenNthCalledWith(2, { page: 2, pageSize: 20 })
    expect(listConversationMessagesMock).toHaveBeenNthCalledWith(2, 11, {
      page: 2,
      pageSize: 50,
      order: 'desc',
    })
    wrapper.unmount()
  })

  it('falls back and locks conversation mode when an old backend parses cursor as an ID', async () => {
    listConversationsCursorMock.mockRejectedValue(
      new ApiError('client', 'conversation ID must be a positive integer', {
        status: 400,
        code: 'invalid_conversation_id',
      }),
    )
    appendUserMessageMock.mockResolvedValue({ created: true, message: userMessage })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    expect(listConversationsCursorMock).toHaveBeenCalledTimes(1)
    expect(listConversationsMock).toHaveBeenCalledTimes(1)

    await wrapper.get('textarea').setValue('保存后刷新会话头部')
    await wrapper.get('.message-composer').trigger('submit')
    await flushPromises()

    expect(listConversationsCursorMock).toHaveBeenCalledTimes(1)
    expect(listConversationsMock).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('does not fall back for an invalid opaque conversation cursor', async () => {
    listConversationsCursorMock.mockRejectedValue(
      new ApiError('client', 'conversation cursor is invalid', {
        status: 400,
        code: 'invalid_conversation_cursor',
      }),
    )

    const wrapper = mount(ConversationsPage)
    await flushPromises()

    expect(listConversationsCursorMock).toHaveBeenCalledTimes(1)
    expect(listConversationsMock).not.toHaveBeenCalled()
    expect(wrapper.get('.error-message').text()).toContain('conversation cursor is invalid')
    wrapper.unmount()
  })

  it('renames the selected conversation and updates the loaded list', async () => {
    updateConversationMock.mockResolvedValue({
      ...conversation,
      title: '重命名后的会话',
      updatedAt: new Date('2026-10-31T02:00:00Z'),
    })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    const renameButton = wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '重命名')
    expect(renameButton).toBeDefined()
    await renameButton!.trigger('click')
    await wrapper.get('input[aria-label="新会话标题"]').setValue('重命名后的会话')
    await wrapper.get('.rename-conversation-form').trigger('submit')
    await flushPromises()

    expect(updateConversationMock).toHaveBeenCalledWith(11, { title: '重命名后的会话' })
    expect(wrapper.get('.conversation-heading h2').text()).toBe('重命名后的会话')
    expect(wrapper.get('.conversation-item strong').text()).toBe('重命名后的会话')
    wrapper.unmount()
  })

  it('archives and restores a conversation while locking the composer and answer parameters', async () => {
    updateConversationMock
      .mockResolvedValueOnce({
        ...conversation,
        status: 'archived',
        updatedAt: new Date('2026-10-31T02:00:00Z'),
      })
      .mockResolvedValueOnce({
        ...conversation,
        status: 'active',
        updatedAt: new Date('2026-10-31T02:01:00Z'),
      })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('归档后不能发送')
    const archiveButton = wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '归档')
    expect(archiveButton).toBeDefined()
    await archiveButton!.trigger('click')
    await flushPromises()

    expect(updateConversationMock).toHaveBeenNthCalledWith(1, 11, { status: 'archived' })
    expect(wrapper.text()).toContain('此会话已归档')
    expect(wrapper.get('textarea').attributes('disabled')).toBeDefined()
    expect(wrapper.get('#document-scope').attributes('disabled')).toBeDefined()
    expect(
      wrapper
        .findAll('.answer-options select')
        .every((select) => select.attributes('disabled') !== undefined),
    ).toBe(true)
    await wrapper.get('.message-composer').trigger('submit')
    expect(queueConversationAnswerMock).not.toHaveBeenCalled()

    const restoreButton = wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '恢复')
    expect(restoreButton).toBeDefined()
    await restoreButton!.trigger('click')
    await flushPromises()

    expect(updateConversationMock).toHaveBeenNthCalledWith(2, 11, { status: 'active' })
    expect(wrapper.find('.archived-notice').exists()).toBe(false)
    expect(wrapper.get('textarea').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('keeps an active answer job and polling when archive returns a safe conflict', async () => {
    getLatestConversationAnswerJobMock.mockResolvedValue(queuedJob)
    getConversationAnswerJobMock.mockResolvedValue(queuedJob)
    listConversationMessagesMock.mockResolvedValue({
      conversationId: 11,
      messages: [userMessage, assistantGenerating],
      pagination: { page: 1, pageSize: 50, total: 2, totalPages: 1 },
    })
    updateConversationMock.mockRejectedValue(
      new ApiError('conflict', '当前会话仍有回答任务，请等待任务结束后再归档。', {
        status: 409,
        code: 'conversation_answer_active',
      }),
    )

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    const archiveButton = wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '归档')
    await archiveButton!.trigger('click')
    await flushPromises()

    expect(wrapper.get('.error-message').text()).toContain('已刷新会话执行状态')
    expect(wrapper.text()).toContain('回答排队中')
    expect(wrapper.get('.secondary-button').text()).toContain('取消回答')
    expect(wrapper.find('.archived-notice').exists()).toBe(false)
    wrapper.unmount()
  })

  it('requires delete confirmation, removes the conversation, and selects the next loaded item', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 20, total: 2, totalPages: 1 },
    })
    listConversationMessagesMock
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 1, pageSize: 50, total: 0, totalPages: 0 },
      })
      .mockResolvedValueOnce({
        conversationId: 12,
        messages: [],
        pagination: { page: 1, pageSize: 50, total: 0, totalPages: 0 },
      })
    getLatestConversationAnswerJobMock.mockResolvedValue(null)
    deleteConversationMock.mockResolvedValue()

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    const deleteButton = wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '删除')
    await deleteButton!.trigger('click')
    expect(deleteConversationMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(
      '删除会话会永久删除消息、关联 Answer Job/Agent Run、来源与 Token，且无法恢复',
    )

    const confirmationButtons = wrapper.findAll('.delete-confirmation button')
    await confirmationButtons[1].trigger('click')
    expect(wrapper.find('.delete-confirmation').exists()).toBe(false)

    await deleteButton!.trigger('click')
    await wrapper
      .findAll('.delete-confirmation button')
      .find((button) => button.text() === '确认删除')!
      .trigger('click')
    await flushPromises()

    expect(deleteConversationMock).toHaveBeenCalledWith(11)
    expect(wrapper.findAll('.conversation-item')).toHaveLength(1)
    expect(wrapper.get('.conversation-heading h2').text()).toBe('第二个会话')
    wrapper.unmount()
  })

  it('shows the empty state after deleting the last loaded conversation', async () => {
    deleteConversationMock.mockResolvedValue()

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '删除')!
      .trigger('click')
    await wrapper
      .findAll('.delete-confirmation button')
      .find((button) => button.text() === '确认删除')!
      .trigger('click')
    await flushPromises()

    expect(wrapper.findAll('.conversation-item')).toHaveLength(0)
    expect(wrapper.get('.empty-panel').text()).toContain('选择一个会话开始')
    wrapper.unmount()
  })

  it('reloads the cursor head after deleting the last loaded item when more exist', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    listConversationsCursorMock
      .mockReset()
      .mockResolvedValueOnce({
        conversations: [conversation],
        cursor: { next: 'older-conversations', hasMore: true },
      })
      .mockResolvedValueOnce({
        conversations: [secondConversation],
        cursor: { next: null, hasMore: false },
      })
    listConversationMessagesCursorMock.mockReset().mockImplementation(async (conversationId) => ({
      conversationId,
      messages: [],
      cursor: { next: null, hasMore: false },
    }))
    deleteConversationMock.mockResolvedValue()

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '删除')!
      .trigger('click')
    await wrapper
      .findAll('.delete-confirmation button')
      .find((button) => button.text() === '确认删除')!
      .trigger('click')
    await flushPromises()

    expect(listConversationsCursorMock).toHaveBeenCalledTimes(2)
    expect(wrapper.get('.conversation-heading h2').text()).toBe('第二个会话')
    expect(wrapper.findAll('.conversation-item')).toHaveLength(1)
    wrapper.unmount()
  })

  it('ignores a late rename response after switching conversations', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const lateRename = deferred<Conversation>()
    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 20, total: 2, totalPages: 1 },
    })
    listConversationMessagesMock.mockImplementation(async (conversationId) => ({
      conversationId,
      messages: [],
      pagination: { page: 1, pageSize: 50, total: 0, totalPages: 0 },
    }))
    updateConversationMock.mockReturnValue(lateRename.promise)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '重命名')!
      .trigger('click')
    await wrapper.get('input[aria-label="新会话标题"]').setValue('迟到标题')
    await wrapper.get('.rename-conversation-form').trigger('submit')
    expect(wrapper.get('textarea').attributes('disabled')).toBeDefined()
    await wrapper.findAll('.conversation-item')[1].trigger('click')
    await flushPromises()

    lateRename.resolve({
      ...conversation,
      title: '迟到标题',
      updatedAt: new Date('2026-10-31T02:00:00Z'),
    })
    await flushPromises()

    expect(wrapper.get('.conversation-heading h2').text()).toBe('第二个会话')
    expect(wrapper.findAll('.conversation-item').map((item) => item.text())).toEqual(
      expect.arrayContaining([expect.stringContaining('迟到标题')]),
    )
    wrapper.unmount()
  })

  it('reconciles a late archive globally while keeping the newer selection', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const lateArchive = deferred<Conversation>()
    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 20, total: 2, totalPages: 1 },
    })
    listConversationMessagesMock.mockImplementation(async (conversationId) => ({
      conversationId,
      messages: conversationId === 11 ? [userMessage, assistantFailed] : [],
      pagination: { page: 1, pageSize: 50, total: conversationId === 11 ? 2 : 0, totalPages: 1 },
    }))
    getLatestConversationAnswerJobMock
      .mockResolvedValueOnce(failedJob)
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce(failedJob)
    updateConversationMock.mockReturnValue(lateArchive.promise)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '归档')!
      .trigger('click')
    expect(wrapper.get('.secondary-button').attributes('disabled')).toBeDefined()
    await wrapper.findAll('.conversation-item')[1].trigger('click')
    await flushPromises()

    lateArchive.resolve({
      ...conversation,
      status: 'archived',
      updatedAt: new Date('2026-10-31T02:00:00Z'),
    })
    await flushPromises()

    expect(wrapper.get('.conversation-heading h2').text()).toBe('第二个会话')
    const archivedItem = wrapper
      .findAll('.conversation-item')
      .find((item) => item.text().includes('研究问题'))
    await archivedItem!.trigger('click')
    await flushPromises()
    expect(wrapper.get('.conversation-status').text()).toBe('archived')
    expect(wrapper.text()).toContain('此会话已归档')
    wrapper.unmount()
  })

  it('does not remove the current UI selection when an old delete response arrives late', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const lateDelete = deferred<void>()
    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 20, total: 2, totalPages: 1 },
    })
    listConversationMessagesMock.mockImplementation(async (conversationId) => ({
      conversationId,
      messages: [],
      pagination: { page: 1, pageSize: 50, total: 0, totalPages: 0 },
    }))
    deleteConversationMock.mockReturnValue(lateDelete.promise)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper
      .findAll('.conversation-management button')
      .find((button) => button.text() === '删除')!
      .trigger('click')
    await wrapper
      .findAll('.delete-confirmation button')
      .find((button) => button.text() === '确认删除')!
      .trigger('click')
    await wrapper.findAll('.conversation-item')[1].trigger('click')
    await flushPromises()

    lateDelete.resolve()
    await flushPromises()

    expect(wrapper.get('.conversation-heading h2').text()).toBe('第二个会话')
    expect(wrapper.findAll('.conversation-item')).toHaveLength(1)
    expect(wrapper.get('.conversation-item').text()).not.toContain('研究问题')
    wrapper.unmount()
  })

  it('falls back to the active endpoint when latest is unavailable during rolling deployment', async () => {
    getLatestConversationAnswerJobMock.mockRejectedValue(
      new ApiError('not-found', 'latest route unavailable', { status: 404 }),
    )
    getActiveConversationAnswerJobMock.mockResolvedValue(queuedJob)
    getConversationAnswerJobMock.mockResolvedValue(queuedJob)
    listConversationMessagesMock.mockResolvedValue({
      conversationId: 11,
      messages: [userMessage, assistantGenerating],
      pagination: { page: 1, pageSize: 50, total: 2, totalPages: 1 },
    })

    const wrapper = mount(ConversationsPage)
    await flushPromises()

    expect(getLatestConversationAnswerJobMock).toHaveBeenCalledWith(11)
    expect(getActiveConversationAnswerJobMock).toHaveBeenCalledWith(11)
    expect(wrapper.text()).toContain('回答排队中')
    wrapper.unmount()
  })

  it('restores a succeeded latest job with sources and token usage', async () => {
    getLatestConversationAnswerJobMock.mockResolvedValue(succeededJob)
    listConversationMessagesMock.mockResolvedValue({
      conversationId: 11,
      messages: [
        userMessage,
        { ...assistantGenerating, content: succeededJob.result?.answer ?? '', status: 'completed' },
      ],
      pagination: { page: 1, pageSize: 50, total: 2, totalPages: 1 },
    })

    const wrapper = mount(ConversationsPage)
    await flushPromises()

    const evidence = wrapper.get('.answer-evidence')
    expect(evidence.text()).toContain('最近回答证据')
    expect(evidence.text()).toContain('研究文档')
    expect(evidence.text()).toContain('随会话保留')
    expect(evidence.text()).toContain('Prompt100')
    expect(evidence.text()).toContain('Total120')
    expect(getConversationAnswerJobMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows evidence-insufficient state for an empty zero-token succeeded result', async () => {
    getLatestConversationAnswerJobMock.mockResolvedValue({
      ...succeededJob,
      result: {
        answer: '没有足够证据。',
        responseLanguage: 'zh',
        sources: [],
        usage: { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
      },
    })

    const wrapper = mount(ConversationsPage)
    await flushPromises()

    expect(wrapper.get('.answer-evidence').text()).toContain('证据不足')
    expect(wrapper.get('.answer-evidence').text()).toContain('没有可展示的检索来源')
    wrapper.unmount()
  })

  it('restores a failed latest job and retries with its original answer parameters', async () => {
    const retriedJob: ConversationAnswerJob = {
      ...queuedJob,
      id: 32,
      documentId: 7,
      userMessageId: 30,
      assistantMessageId: 31,
      topK: 8,
      requestedResponseLanguage: 'zh',
    }
    getLatestConversationAnswerJobMock.mockResolvedValue(failedJob)
    listConversationMessagesMock.mockResolvedValue({
      conversationId: 11,
      messages: [userMessage, assistantFailed],
      pagination: { page: 1, pageSize: 50, total: 2, totalPages: 1 },
    })
    queueConversationAnswerMock.mockResolvedValue({
      conversationId: 11,
      userMessage: { ...userMessage, id: 30, sequenceNo: 3 },
      assistantMessage: { ...assistantGenerating, id: 31, sequenceNo: 4 },
      answerJob: retriedJob,
    })
    getConversationAnswerJobMock.mockResolvedValue(retriedJob)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    expect(wrapper.get('.secondary-button').text()).toContain('再次提交此问题')

    await wrapper.get('.secondary-button').trigger('click')
    await flushPromises()

    expect(queueConversationAnswerMock).toHaveBeenCalledWith(11, {
      content: failedJob.query,
      clientMessageId: expect.any(String),
      documentId: 7,
      topK: 8,
      responseLanguage: 'zh',
    })
    wrapper.unmount()
  })

  it('keeps a non-preset historical top-k visible in the full 1-to-20 selector', async () => {
    getLatestConversationAnswerJobMock.mockResolvedValue({ ...failedJob, topK: 17 })
    listConversationMessagesMock.mockResolvedValue({
      conversationId: 11,
      messages: [userMessage, assistantFailed],
      pagination: { page: 1, pageSize: 50, total: 2, totalPages: 1 },
    })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')

    const topKSelect = wrapper.findAll('.answer-options select')[1]
    expect((topKSelect.element as HTMLSelectElement).value).toBe('17')
    expect(topKSelect.findAll('option')).toHaveLength(20)
    expect(topKSelect.text()).toContain('17 条')
    wrapper.unmount()
  })

  it('submits the selected document scope, top-k, and response language', async () => {
    const parameterizedJob: ConversationAnswerJob = {
      ...queuedJob,
      documentId: 7,
      topK: 8,
      requestedResponseLanguage: 'en',
    }
    queueConversationAnswerMock.mockResolvedValue({
      conversationId: 11,
      userMessage,
      assistantMessage: assistantGenerating,
      answerJob: parameterizedJob,
    })
    getConversationAnswerJobMock.mockResolvedValue(parameterizedJob)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('#document-scope').setValue('document:7')
    const optionSelects = wrapper.findAll('.answer-options select')
    await optionSelects[0].setValue('en')
    await optionSelects[1].setValue('8')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(queueConversationAnswerMock).toHaveBeenCalledWith(11, {
      content: '解释这份文档',
      clientMessageId: expect.any(String),
      documentId: 7,
      topK: 8,
      responseLanguage: 'en',
    })
    wrapper.unmount()
  })

  it('restores an active queued answer job when the conversation page opens', async () => {
    getLatestConversationAnswerJobMock.mockResolvedValue(queuedJob)
    getConversationAnswerJobMock.mockResolvedValue(queuedJob)
    listConversationMessagesMock.mockResolvedValue({
      conversationId: 11,
      messages: [userMessage, assistantGenerating],
      pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
    })

    const wrapper = mount(ConversationsPage)
    await flushPromises()

    expect(getLatestConversationAnswerJobMock).toHaveBeenCalledWith(11)
    expect(wrapper.text()).toContain('回答排队中')
    expect(wrapper.get('.secondary-button').text()).toContain('取消回答')
    wrapper.unmount()
  })

  it('restores a processing answer job with the non-cancelable status copy', async () => {
    const processingJob: ConversationAnswerJob = {
      ...queuedJob,
      status: 'processing',
      cancelable: false,
      startedAt: new Date('2026-10-31T01:01:30Z'),
    }
    getLatestConversationAnswerJobMock.mockResolvedValue(processingJob)
    getConversationAnswerJobMock.mockResolvedValue(processingJob)
    listConversationMessagesMock.mockResolvedValue({
      conversationId: 11,
      messages: [userMessage, assistantGenerating],
      pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
    })

    const wrapper = mount(ConversationsPage)
    await flushPromises()

    expect(wrapper.text()).toContain('回答生成中')
    expect(wrapper.text()).toContain('Worker 已开始处理，当前不能取消。')
    expect(wrapper.find('.secondary-button').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps a newer conversation selection when an older restore request finishes late', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const lateMessages = deferred<Awaited<ReturnType<typeof listConversationMessages>>>()
    const secondMessage: ConversationMessage = {
      ...userMessage,
      id: 40,
      conversationId: 12,
      content: '第二个会话消息',
    }

    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
    })
    listConversationMessagesMock
      .mockReset()
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 1, pageSize: 100, total: 0, totalPages: 0 },
      })
      .mockReturnValueOnce(lateMessages.promise)
      .mockResolvedValueOnce({
        conversationId: 12,
        messages: [secondMessage],
        pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
      })
    getLatestConversationAnswerJobMock
      .mockReset()
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce(null)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    const conversationButtons = wrapper.findAll('.conversation-item')
    await conversationButtons[0].trigger('click')
    await conversationButtons[1].trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('第二个会话消息')

    lateMessages.resolve({
      conversationId: 11,
      messages: [{ ...userMessage, content: '第一个会话迟到消息' }],
      pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('第二个会话消息')
    expect(wrapper.text()).not.toContain('第一个会话迟到消息')
    expect(wrapper.find('.answer-job-status').exists()).toBe(false)
    expect(getConversationAnswerJobMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('ignores a late active job response from an older conversation selection', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const lateActiveJob = deferred<ConversationAnswerJob | null>()
    const secondMessage: ConversationMessage = {
      ...userMessage,
      id: 40,
      conversationId: 12,
      content: '第二个会话消息',
    }

    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
    })
    getLatestConversationAnswerJobMock
      .mockReset()
      .mockResolvedValueOnce(null)
      .mockReturnValueOnce(lateActiveJob.promise)
      .mockResolvedValueOnce(null)
    listConversationMessagesMock
      .mockReset()
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 1, pageSize: 100, total: 0, totalPages: 0 },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 1, pageSize: 100, total: 0, totalPages: 0 },
      })
      .mockResolvedValueOnce({
        conversationId: 12,
        messages: [secondMessage],
        pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
      })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    const conversationButtons = wrapper.findAll('.conversation-item')
    await conversationButtons[0].trigger('click')
    await conversationButtons[1].trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('第二个会话消息')

    lateActiveJob.resolve(queuedJob)
    await flushPromises()

    expect(wrapper.text()).toContain('第二个会话消息')
    expect(wrapper.find('.answer-job-status').exists()).toBe(false)
    expect(listConversationMessagesMock).toHaveBeenCalledTimes(3)
    expect(getConversationAnswerJobMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('treats a missing active-job route as empty when messages still load', async () => {
    getLatestConversationAnswerJobMock.mockRejectedValue(
      new ApiError('not-found', '请求的资源不存在。', { status: 404 }),
    )
    getActiveConversationAnswerJobMock.mockRejectedValue(
      new ApiError('not-found', '请求的资源不存在。', { status: 404 }),
    )
    listConversationMessagesMock.mockResolvedValue({
      conversationId: 11,
      messages: [userMessage],
      pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
    })

    const wrapper = mount(ConversationsPage)
    await flushPromises()

    expect(wrapper.text()).toContain('解释这份文档')
    expect(wrapper.find('.error-message').exists()).toBe(false)
    expect(wrapper.find('.answer-job-status').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows the message error when both the active job and messages are missing', async () => {
    getLatestConversationAnswerJobMock.mockRejectedValue(
      new ApiError('not-found', '活动任务入口不存在。', { status: 404 }),
    )
    getActiveConversationAnswerJobMock.mockRejectedValue(
      new ApiError('not-found', '活动任务入口不存在。', { status: 404 }),
    )
    listConversationMessagesMock.mockRejectedValue(
      new ApiError('not-found', '会话不存在或无权访问。', { status: 404 }),
    )

    const wrapper = mount(ConversationsPage)
    await flushPromises()

    expect(wrapper.get('.error-message').text()).toContain('会话不存在或无权访问。')
    wrapper.unmount()
  })

  it('does not merge a late send response after switching conversations', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const secondMessage: ConversationMessage = {
      ...userMessage,
      id: 40,
      conversationId: 12,
      content: '第二个会话消息',
    }
    const lateSend = deferred<Awaited<ReturnType<typeof queueConversationAnswer>>>()

    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
    })
    listConversationMessagesMock
      .mockReset()
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 1, pageSize: 100, total: 0, totalPages: 0 },
      })
      .mockResolvedValueOnce({
        conversationId: 12,
        messages: [secondMessage],
        pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
      })
    queueConversationAnswerMock.mockReturnValue(lateSend.promise)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('form').trigger('submit')
    expect(
      wrapper
        .findAll('.conversation-management button')
        .filter((button) => ['归档', '删除'].includes(button.text()))
        .every((button) => button.attributes('disabled') !== undefined),
    ).toBe(true)
    await wrapper.findAll('.conversation-item')[1].trigger('click')
    await flushPromises()

    lateSend.resolve({
      conversationId: 11,
      userMessage,
      assistantMessage: assistantGenerating,
      answerJob: queuedJob,
    })
    await flushPromises()

    expect(wrapper.text()).toContain('第二个会话消息')
    expect(wrapper.text()).not.toContain('正在生成回答…')
    expect(wrapper.find('.answer-job-status').exists()).toBe(false)
    expect(getConversationAnswerJobMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('does not apply a late retry response after switching conversations', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const secondMessage: ConversationMessage = {
      ...userMessage,
      id: 40,
      conversationId: 12,
      content: '第二个会话消息',
    }
    const lateRetry = deferred<Awaited<ReturnType<typeof queueConversationAnswer>>>()

    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
    })
    listConversationMessagesMock
      .mockReset()
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 1, pageSize: 100, total: 0, totalPages: 0 },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [userMessage, assistantFailed],
        pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
      })
      .mockResolvedValueOnce({
        conversationId: 12,
        messages: [secondMessage],
        pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
      })
    queueConversationAnswerMock
      .mockResolvedValueOnce({
        conversationId: 11,
        userMessage,
        assistantMessage: assistantGenerating,
        answerJob: queuedJob,
      })
      .mockReturnValueOnce(lateRetry.promise)
    getConversationAnswerJobMock.mockResolvedValueOnce(failedJob)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await flushPromises()
    expect(getConversationAnswerJobMock).toHaveBeenCalledTimes(1)
    expect(wrapper.get('.secondary-button').text()).toContain('再次提交此问题')

    await wrapper.get('.secondary-button').trigger('click')
    expect(
      wrapper
        .findAll('.conversation-management button')
        .filter((button) => ['归档', '删除'].includes(button.text()))
        .every((button) => button.attributes('disabled') !== undefined),
    ).toBe(true)
    await wrapper.findAll('.conversation-item')[1].trigger('click')
    await flushPromises()

    lateRetry.resolve({
      conversationId: 11,
      userMessage: { ...userMessage, id: 30, sequenceNo: 3 },
      assistantMessage: { ...assistantGenerating, id: 31, sequenceNo: 4 },
      answerJob: { ...queuedJob, id: 32, userMessageId: 30, assistantMessageId: 31 },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('第二个会话消息')
    expect(wrapper.find('.answer-job-status').exists()).toBe(false)
    expect(getConversationAnswerJobMock).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('does not apply a late cancel response after switching conversations', async () => {
    const secondConversation: Conversation = {
      ...conversation,
      id: 12,
      title: '第二个会话',
      updatedAt: new Date('2026-10-31T00:30:00Z'),
    }
    const secondMessage: ConversationMessage = {
      ...userMessage,
      id: 40,
      conversationId: 12,
      content: '第二个会话消息',
    }
    const lateCancel = deferred<ConversationAnswerJob>()

    listConversationsMock.mockResolvedValue({
      conversations: [conversation, secondConversation],
      pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
    })
    getLatestConversationAnswerJobMock
      .mockReset()
      .mockResolvedValueOnce(queuedJob)
      .mockResolvedValueOnce(null)
    getConversationAnswerJobMock.mockResolvedValue(queuedJob)
    listConversationMessagesMock
      .mockReset()
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [userMessage, assistantGenerating],
        pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
      })
      .mockResolvedValueOnce({
        conversationId: 12,
        messages: [secondMessage],
        pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
      })
    cancelConversationAnswerJobMock.mockReturnValue(lateCancel.promise)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper.get('.secondary-button').trigger('click')
    expect(
      wrapper
        .findAll('.conversation-management button')
        .filter((button) => ['归档', '删除'].includes(button.text()))
        .every((button) => button.attributes('disabled') !== undefined),
    ).toBe(true)
    await wrapper.findAll('.conversation-item')[1].trigger('click')
    await flushPromises()

    lateCancel.resolve(canceledJob)
    await flushPromises()

    expect(wrapper.text()).toContain('第二个会话消息')
    expect(wrapper.text()).not.toContain('回答任务已取消。')
    expect(wrapper.find('.answer-job-status').exists()).toBe(false)
    wrapper.unmount()
  })

  it('invalidates an in-flight poll before applying a successful cancel response', async () => {
    const latePoll = deferred<ConversationAnswerJob>()
    const processingJob: ConversationAnswerJob = {
      ...queuedJob,
      status: 'processing',
      cancelable: false,
      startedAt: new Date('2026-10-31T01:01:30Z'),
    }
    getLatestConversationAnswerJobMock.mockResolvedValue(queuedJob)
    getConversationAnswerJobMock.mockReturnValue(latePoll.promise)
    cancelConversationAnswerJobMock.mockResolvedValue(canceledJob)
    listConversationMessagesMock
      .mockReset()
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [userMessage, assistantGenerating],
        pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [assistantCanceled],
        pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
      })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper.get('.secondary-button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('回答任务已取消。')
    expect(wrapper.text()).toContain('解释这份文档')
    expect(wrapper.get('.secondary-button').text()).toContain('再次提交此问题')

    latePoll.resolve(processingJob)
    await flushPromises()

    expect(wrapper.text()).not.toContain('回答生成中')
    expect(wrapper.text()).not.toContain('Worker 已开始处理')
    expect(wrapper.get('.secondary-button').text()).toContain('再次提交此问题')
    wrapper.unmount()
  })

  it('allows canceling a queued answer job and refreshes the interrupted assistant message', async () => {
    queueConversationAnswerMock.mockResolvedValue({
      conversationId: 11,
      userMessage,
      assistantMessage: assistantGenerating,
      answerJob: queuedJob,
    })
    getConversationAnswerJobMock.mockResolvedValue(queuedJob)
    cancelConversationAnswerJobMock.mockResolvedValue(canceledJob)
    listConversationMessagesMock
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 1, pageSize: 100, total: 0, totalPages: 0 },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [userMessage, assistantCanceled],
        pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
      })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('.secondary-button').text()).toContain('取消回答')
    await wrapper.get('.secondary-button').trigger('click')
    await flushPromises()

    expect(cancelConversationAnswerJobMock).toHaveBeenCalledWith(31)
    expect(wrapper.text()).toContain('回答任务已取消')
    expect(wrapper.text()).toContain('回答已取消。')
    wrapper.unmount()
  })

  it('shows answer queue capacity and blocks resubmission during Retry-After', async () => {
    vi.useFakeTimers()
    queueConversationAnswerMock.mockRejectedValueOnce(
      new ApiError('rate-limited', 'answer queue full', {
        status: 429,
        code: 'answer_job_owner_queue_capacity',
        requestId: 'answer-capacity-ui-1',
        retryAfterSeconds: 5,
      }),
    )

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('.capacity-message').text()).toContain('当前账户的问答队列已满')
    expect(wrapper.get('.capacity-message').text()).toContain('5 秒后可再次提交')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()

    await vi.advanceTimersByTimeAsync(5_000)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('resubmits a failed question as a new conversation turn', async () => {
    const retryUserMessage = {
      ...userMessage,
      id: 30,
      sequenceNo: 3,
      clientMessageId: 'client-retry',
    }
    const retryAssistantMessage = {
      ...assistantGenerating,
      id: 31,
      sequenceNo: 4,
    }
    const retryJob = { ...queuedJob, id: 32, userMessageId: 30, assistantMessageId: 31 }
    queueConversationAnswerMock
      .mockResolvedValueOnce({
        conversationId: 11,
        userMessage,
        assistantMessage: assistantGenerating,
        answerJob: queuedJob,
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        userMessage: retryUserMessage,
        assistantMessage: retryAssistantMessage,
        answerJob: retryJob,
      })
    getConversationAnswerJobMock.mockResolvedValueOnce(failedJob).mockResolvedValue(retryJob)
    listConversationMessagesMock
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [],
        pagination: { page: 1, pageSize: 100, total: 0, totalPages: 0 },
      })
      .mockResolvedValueOnce({
        conversationId: 11,
        messages: [userMessage, assistantFailed],
        pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
      })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('.secondary-button').text()).toContain('再次提交此问题')
    await wrapper.get('.secondary-button').trigger('click')
    await flushPromises()

    expect(queueConversationAnswerMock).toHaveBeenCalledTimes(2)
    expect(queueConversationAnswerMock).toHaveBeenNthCalledWith(2, 11, {
      content: '解释这份文档',
      clientMessageId: expect.any(String),
      documentId: 7,
      topK: 8,
      responseLanguage: 'zh',
    })
    wrapper.unmount()
  })

  it('uses mutually exclusive composer modes and creates an Agent Run without Answer fields', async () => {
    const latePoll = deferred<AgentRun>()
    createAgentRunMock.mockResolvedValue({
      conversationId: 11,
      userMessage,
      assistantMessage: assistantGenerating,
      agentRun: queuedAgentRun,
    })
    getAgentRunMock.mockReturnValue(latePoll.promise)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    expect(wrapper.findAll('input[name="composer-mode"]')).toHaveLength(3)
    await selectComposerMode(wrapper, 'agent')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('.message-composer').trigger('submit')
    await flushPromises()

    expect(createAgentRunMock).toHaveBeenCalledWith(
      11,
      {
        content: '解释这份文档',
        clientRunId: expect.any(String),
        documentId: undefined,
        responseLanguage: 'auto',
      },
      expect.any(AbortSignal),
    )
    expect(queueConversationAnswerMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Agent Run #71')
    expect(wrapper.text()).toContain('等待 Worker 领取任务')
    expect(wrapper.text()).toContain('正在生成回答…')
    wrapper.unmount()
  })

  it('hides Agent after a generic route 404 while message and RAG modes remain usable', async () => {
    getLatestAgentRunMock.mockRejectedValueOnce(
      new ApiError('not-found', 'route missing', { status: 404 }),
    )
    queueConversationAnswerMock.mockResolvedValue({
      conversationId: 11,
      userMessage,
      assistantMessage: assistantGenerating,
      answerJob: queuedJob,
    })
    getConversationAnswerJobMock.mockResolvedValue(queuedJob)
    appendUserMessageMock.mockResolvedValue({ created: true, message: userMessage })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    expect(wrapper.find('input[value="agent"]').exists()).toBe(false)
    expect(wrapper.find('input[value="message"]').exists()).toBe(true)
    expect(wrapper.find('input[value="answer"]').exists()).toBe(true)

    await wrapper.get('textarea').setValue('只保存这条消息')
    await wrapper.get('.message-composer').trigger('submit')
    await flushPromises()
    expect(appendUserMessageMock).toHaveBeenCalledTimes(1)

    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('.message-composer').trigger('submit')
    await flushPromises()
    expect(queueConversationAnswerMock).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('restores active and terminal Agent snapshots without enabling a second execution', async () => {
    const latePoll = deferred<AgentRun>()
    getLatestAgentRunMock.mockResolvedValueOnce(processingAgentRun)
    getAgentRunMock.mockReturnValue(latePoll.promise)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    expect(wrapper.text()).toContain('Agent Run #71')
    expect(wrapper.text()).toContain('执行中，当前不可取消')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()

    getLatestAgentRunMock.mockResolvedValueOnce(succeededAgentRun)
    const terminalWrapper = mount(ConversationsPage)
    await flushPromises()
    expect(terminalWrapper.text()).toContain('Agent 最终回答')
    expect(terminalWrapper.text()).toContain('研究文档')
    expect(terminalWrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await terminalWrapper.get('textarea').setValue('新问题')
    expect(terminalWrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    terminalWrapper.unmount()
  })

  it.each(['conversation_execution_active', 'conversation_answer_active'])(
    'reconciles both execution conflict codes without replaying create: %s',
    async (code) => {
      createAgentRunMock.mockRejectedValueOnce(
        new ApiError('conflict', 'busy', { status: 409, code }),
      )
      getActiveAgentRunMock.mockResolvedValueOnce(null)
      getLatestAgentRunMock.mockResolvedValueOnce(null).mockResolvedValueOnce(null)
      getActiveConversationAnswerJobMock.mockResolvedValueOnce(null)
      getLatestConversationAnswerJobMock.mockResolvedValue(null)

      const wrapper = mount(ConversationsPage)
      await flushPromises()
      await selectComposerMode(wrapper, 'agent')
      await wrapper.get('textarea').setValue('保留这个问题')
      await wrapper.get('.message-composer').trigger('submit')
      await flushPromises()

      expect(createAgentRunMock).toHaveBeenCalledTimes(1)
      expect(getActiveAgentRunMock).toHaveBeenCalledWith(11, expect.any(AbortSignal))
      expect(getActiveConversationAnswerJobMock).toHaveBeenCalledWith(11)
      expect(getLatestAgentRunMock).toHaveBeenCalledTimes(2)
      expect(getLatestConversationAnswerJobMock).toHaveBeenCalledTimes(2)
      expect(wrapper.get('textarea').element.value).toBe('保留这个问题')
      wrapper.unmount()
    },
  )

  it('reconciles a legacy Answer create conflict through both active endpoints', async () => {
    queueConversationAnswerMock.mockRejectedValueOnce(
      new ApiError('conflict', 'busy', {
        status: 409,
        code: 'conversation_answer_active',
      }),
    )
    getActiveConversationAnswerJobMock.mockResolvedValueOnce(null)
    getActiveAgentRunMock.mockResolvedValueOnce(null)
    getLatestConversationAnswerJobMock.mockResolvedValue(null)
    getLatestAgentRunMock.mockResolvedValueOnce(null).mockResolvedValueOnce(null)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'answer')
    await wrapper.get('textarea').setValue('保留 RAG 问题')
    await wrapper.get('.message-composer').trigger('submit')
    await flushPromises()

    expect(queueConversationAnswerMock).toHaveBeenCalledTimes(1)
    expect(getActiveConversationAnswerJobMock).toHaveBeenCalledWith(11)
    expect(getActiveAgentRunMock).toHaveBeenCalledWith(11, expect.any(AbortSignal))
    expect(wrapper.get('textarea').element.value).toBe('保留 RAG 问题')
    wrapper.unmount()
  })

  it('blocks submission until Agent restore resolves', async () => {
    const lateLatest = deferred<AgentRun | null>()
    getLatestAgentRunMock.mockReturnValueOnce(lateLatest.promise)
    appendUserMessageMock.mockResolvedValue({ created: true, message: userMessage })

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await wrapper.get('textarea').setValue('恢复期间不发送')
    await wrapper.get('.message-composer').trigger('submit')
    expect(appendUserMessageMock).not.toHaveBeenCalled()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()

    lateLatest.resolve(processingAgentRun)
    await flushPromises()
    expect(wrapper.text()).toContain('Agent Run #71')
    expect(appendUserMessageMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('fails closed when an active lookup fails and offers manual reconciliation', async () => {
    createAgentRunMock.mockRejectedValueOnce(
      new ApiError('conflict', 'busy', {
        status: 409,
        code: 'conversation_execution_active',
      }),
    )
    getActiveConversationAnswerJobMock
      .mockRejectedValueOnce(new ApiError('server', 'lookup failed', { status: 500 }))
      .mockResolvedValueOnce(null)
    getActiveAgentRunMock.mockResolvedValue(null)
    getLatestConversationAnswerJobMock.mockResolvedValue(null)
    getLatestAgentRunMock.mockResolvedValueOnce(null).mockResolvedValueOnce(null)

    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'agent')
    await wrapper.get('textarea').setValue('保留问题')
    await wrapper.get('.message-composer').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('重新检查执行状态')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('.execution-reconcile-button').trigger('click')
    await flushPromises()
    expect(wrapper.find('.execution-reconcile-button').exists()).toBe(false)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('shows Agent capacity, Request ID, and never auto replays after cooldown', async () => {
    vi.useFakeTimers()
    createAgentRunMock.mockRejectedValueOnce(
      new ApiError('rate-limited', 'agent owner capacity', {
        status: 429,
        code: 'agent_run_owner_queue_capacity',
        requestId: 'agent-capacity-ui-1',
        retryAfterSeconds: 5,
      }),
    )
    const wrapper = mount(ConversationsPage)
    await flushPromises()
    await selectComposerMode(wrapper, 'agent')
    await wrapper.get('textarea').setValue('解释这份文档')
    await wrapper.get('.message-composer').trigger('submit')
    await flushPromises()

    expect(wrapper.get('.capacity-message').text()).toContain('当前账户的 Agent 队列已满')
    expect(wrapper.get('.capacity-message').text()).toContain('请求编号：agent-capacity-ui-1')
    await vi.advanceTimersByTimeAsync(5_000)
    expect(createAgentRunMock).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
