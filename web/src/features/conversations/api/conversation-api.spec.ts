import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'
import {
  appendUserMessage,
  cancelConversationAnswerJob,
  createConversation,
  deleteConversation,
  getConversationAnswerJob,
  getLatestConversationAnswerJob,
  listConversationMessages,
  listConversationMessagesCursor,
  listConversations,
  listConversationsCursor,
  mapConversationAnswerStartResponse,
  mapConversationCursorResponse,
  mapConversationMessageCursorResponse,
  mapConversationMessagePageResponse,
  mapConversationPageResponse,
  queueConversationAnswer,
  updateConversation,
} from './conversation-api'

vi.mock('../../../shared/api/http-client', () => ({
  httpClient: {
    get: vi.fn(),
    post: vi.fn(),
    patch: vi.fn(),
    delete: vi.fn(),
  },
}))

const getMock = vi.mocked(httpClient.get)
const postMock = vi.mocked(httpClient.post)
const patchMock = vi.mocked(httpClient.patch)
const deleteMock = vi.mocked(httpClient.delete)

const conversation = {
  id: 11,
  title: 'New conversation',
  status: 'active',
  last_message_at: null,
  created_at: '2026-10-30T12:00:00Z',
  updated_at: '2026-10-30T12:00:00Z',
}

const message = {
  id: 20,
  conversation_id: 11,
  sequence_no: 1,
  role: 'user',
  content: 'hello',
  status: 'completed',
  client_message_id: 'client-1',
  created_at: '2026-10-30T12:01:00Z',
  updated_at: '2026-10-30T12:01:00Z',
}

const pagination = { page: 1, page_size: 100, total: 1, total_pages: 1 }
const answerJob = {
  id: 31,
  document_id: null,
  conversation_id: 11,
  user_message_id: 20,
  assistant_message_id: 21,
  query: 'explain',
  top_k: 5,
  requested_response_language: 'auto',
  status: 'queued',
  cancelable: true,
  attempt_count: 0,
  error_message: null,
  result: null,
  next_attempt_at: '2026-10-30T12:01:00Z',
  created_at: '2026-10-30T12:01:00Z',
  updated_at: '2026-10-30T12:01:00Z',
  started_at: null,
  completed_at: null,
}
const succeededAnswerJob = {
  ...answerJob,
  status: 'succeeded',
  cancelable: false,
  result: {
    answer: 'grounded answer',
    response_language: 'zh',
    sources: [
      {
        citation: 1,
        chunk_id: 501,
        document_id: 7,
        chunk_index: 2,
        title: 'Study',
        original_name: 'study.pdf',
        page_start: 3,
        page_end: 4,
        similarity: 0.88,
      },
    ],
    usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 120 },
  },
  completed_at: '2026-10-30T12:02:00Z',
}
const assistantMessage = {
  ...message,
  id: 21,
  role: 'assistant',
  status: 'generating',
  content: '正在生成回答…',
}

beforeEach(() => {
  getMock.mockReset()
  postMock.mockReset()
  patchMock.mockReset()
  deleteMock.mockReset()
})

describe('conversation response mapping', () => {
  it('maps conversations, messages, dates, and pagination', () => {
    expect(
      mapConversationPageResponse({ conversations: [conversation], pagination }),
    ).toMatchObject({
      conversations: [{ id: 11, title: 'New conversation', status: 'active' }],
      pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
    })
    expect(
      mapConversationMessagePageResponse({ conversation_id: 11, messages: [message], pagination }),
    ).toMatchObject({
      conversationId: 11,
      messages: [{ id: 20, sequenceNo: 1, role: 'user', content: 'hello' }],
    })
  })

  it('rejects malformed responses', () => {
    expect(() =>
      mapConversationPageResponse({ conversations: [], pagination: { page: 0 } }),
    ).toThrow(new ApiError('invalid-response', '后端会话列表响应不符合约定。'))
    expect(() =>
      mapConversationMessagePageResponse({ conversation_id: 11, messages: [], pagination }),
    ).not.toThrow()
    expect(() =>
      mapConversationMessagePageResponse({
        conversation_id: 11,
        messages: [{ ...message, sequence_no: 0 }],
        pagination,
      }),
    ).toThrow(new ApiError('invalid-response', '后端会话消息响应不符合约定。'))
    expect(() =>
      mapConversationAnswerStartResponse({
        conversation_id: 11,
        user_message: message,
        assistant_message: assistantMessage,
        answer_job: answerJob,
      }),
    ).not.toThrow()
    expect(() => mapConversationAnswerStartResponse({ conversation_id: 11 })).toThrow(
      new ApiError('invalid-response', '后端会话回答任务响应不符合约定。'),
    )
  })

  it('rejects inconsistent answer start relationships and message roles', () => {
    const valid = {
      conversation_id: 11,
      user_message: message,
      assistant_message: assistantMessage,
      answer_job: answerJob,
    }
    const invalidResponses = [
      { ...valid, user_message: { ...message, conversation_id: 12 } },
      { ...valid, assistant_message: { ...assistantMessage, conversation_id: 12 } },
      { ...valid, answer_job: { ...answerJob, conversation_id: 12 } },
      { ...valid, answer_job: { ...answerJob, user_message_id: 999 } },
      { ...valid, answer_job: { ...answerJob, assistant_message_id: 999 } },
      { ...valid, user_message: { ...message, role: 'assistant' } },
      { ...valid, assistant_message: { ...assistantMessage, role: 'user' } },
    ]

    for (const response of invalidResponses) {
      expect(() => mapConversationAnswerStartResponse(response)).toThrow(
        new ApiError('invalid-response', '后端会话回答任务关联关系不符合约定。'),
      )
    }
  })

  it('rejects invalid terminal result, token, citation, and page-range contracts', () => {
    const invalidJobs = [
      { ...succeededAnswerJob, completed_at: null },
      {
        ...succeededAnswerJob,
        result: { ...succeededAnswerJob.result, response_language: 'auto' },
      },
      {
        ...succeededAnswerJob,
        result: {
          ...succeededAnswerJob.result,
          usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 121 },
        },
      },
      {
        ...succeededAnswerJob,
        result: {
          ...succeededAnswerJob.result,
          sources: [{ ...succeededAnswerJob.result.sources[0], citation: 2 }],
        },
      },
      {
        ...succeededAnswerJob,
        result: {
          ...succeededAnswerJob.result,
          sources: [{ ...succeededAnswerJob.result.sources[0], page_start: 4, page_end: 3 }],
        },
      },
      {
        ...answerJob,
        status: 'failed',
        cancelable: false,
        completed_at: null,
      },
    ]

    for (const invalidJob of invalidJobs) {
      expect(() =>
        mapConversationAnswerStartResponse({
          conversation_id: 11,
          user_message: message,
          assistant_message: assistantMessage,
          answer_job: invalidJob,
        }),
      ).toThrow(new ApiError('invalid-response', '后端会话回答任务响应不符合约定。'))
    }
  })

  it('maps opaque cursor responses and rejects inconsistent cursor metadata', () => {
    expect(
      mapConversationCursorResponse({
        conversations: [conversation],
        cursor: { next: 'opaque-conversation-cursor', has_more: true },
      }),
    ).toMatchObject({
      conversations: [{ id: 11 }],
      cursor: { next: 'opaque-conversation-cursor', hasMore: true },
    })
    expect(
      mapConversationMessageCursorResponse({
        conversation_id: 11,
        messages: [message, { ...assistantMessage, sequence_no: 2 }],
        cursor: { next: null, has_more: false },
      }),
    ).toMatchObject({
      conversationId: 11,
      messages: [{ sequenceNo: 1 }, { sequenceNo: 2 }],
      cursor: { next: null, hasMore: false },
    })

    for (const cursor of [
      { next: '', has_more: true },
      { next: null, has_more: true },
      { next: 'opaque', has_more: false },
      { next: 123, has_more: true },
    ]) {
      expect(() => mapConversationCursorResponse({ conversations: [], cursor })).toThrow(
        new ApiError('invalid-response', '后端会话游标响应不符合约定。'),
      )
    }
    expect(() =>
      mapConversationMessageCursorResponse({
        conversation_id: 11,
        messages: [{ ...assistantMessage, sequence_no: 2 }, message],
        cursor: { next: null, has_more: false },
      }),
    ).toThrow(new ApiError('invalid-response', '后端会话消息游标响应不符合约定。'))
    expect(() =>
      mapConversationMessageCursorResponse({
        conversation_id: 11,
        messages: [{ ...message, conversation_id: 12 }],
        cursor: { next: null, has_more: false },
      }),
    ).toThrow(new ApiError('invalid-response', '后端会话消息游标响应不符合约定。'))
  })
})

describe('conversation API calls', () => {
  it('uses the protected conversation endpoints and preserves idempotency fields', async () => {
    getMock.mockResolvedValueOnce({ data: { conversations: [conversation], pagination } })
    postMock.mockResolvedValueOnce({ data: conversation, status: 201 })
    postMock.mockResolvedValueOnce({ data: { created: true, message }, status: 201 })
    postMock.mockResolvedValueOnce({
      data: {
        conversation_id: 11,
        user_message: message,
        assistant_message: assistantMessage,
        answer_job: answerJob,
      },
      status: 202,
    })
    getMock.mockResolvedValueOnce({
      data: { conversation_id: 11, messages: [message], pagination },
    })
    getMock.mockResolvedValueOnce({ data: answerJob })

    await listConversations({ page: 1, pageSize: 20 })
    await createConversation()
    await appendUserMessage(11, 'hello', 'client-1')
    await queueConversationAnswer(11, {
      content: 'explain',
      clientMessageId: 'client-2',
      documentId: 7,
      topK: 8,
      responseLanguage: 'zh',
    })
    await listConversationMessages(11, { page: 1, pageSize: 50, order: 'desc' })
    await getConversationAnswerJob(31)

    expect(getMock).toHaveBeenNthCalledWith(1, '/conversations', {
      params: { page: 1, page_size: 20 },
      signal: undefined,
    })
    expect(postMock).toHaveBeenNthCalledWith(
      1,
      '/conversations',
      { title: '' },
      { signal: undefined },
    )
    expect(postMock).toHaveBeenNthCalledWith(
      2,
      '/conversations/11/messages',
      { content: 'hello', client_message_id: 'client-1' },
      { signal: undefined },
    )
    expect(postMock).toHaveBeenNthCalledWith(
      3,
      '/conversations/11/answer-jobs',
      {
        content: 'explain',
        client_message_id: 'client-2',
        document_id: 7,
        top_k: 8,
        response_language: 'zh',
      },
      { signal: undefined },
    )
    expect(getMock).toHaveBeenNthCalledWith(2, '/conversations/11/messages', {
      params: { page: 1, page_size: 50, order: 'desc' },
      signal: undefined,
    })
    expect(getMock).toHaveBeenNthCalledWith(3, '/answer-jobs/31', { signal: undefined })
  })

  it('uses opaque cursors without decoding them', async () => {
    getMock
      .mockResolvedValueOnce({
        data: {
          conversations: [conversation],
          cursor: { next: 'opaque-next-conversation', has_more: true },
        },
      })
      .mockResolvedValueOnce({
        data: {
          conversation_id: 11,
          messages: [message],
          cursor: { next: 'opaque-next-message', has_more: true },
        },
      })

    await listConversationsCursor({ pageSize: 20, cursor: 'opaque-current-conversation' })
    await listConversationMessagesCursor(11, {
      pageSize: 50,
      cursor: 'opaque-current-message',
    })

    expect(getMock).toHaveBeenNthCalledWith(1, '/conversations/cursor', {
      params: { page_size: 20, cursor: 'opaque-current-conversation' },
      signal: undefined,
    })
    expect(getMock).toHaveBeenNthCalledWith(2, '/conversations/11/messages/cursor', {
      params: { page_size: 50, cursor: 'opaque-current-message' },
      signal: undefined,
    })
  })

  it('cancels a queued answer job through the protected endpoint', async () => {
    postMock.mockResolvedValueOnce({
      data: {
        ...answerJob,
        status: 'canceled',
        cancelable: false,
        completed_at: '2026-10-30T12:02:00Z',
      },
      status: 200,
    })

    const result = await cancelConversationAnswerJob(31)

    expect(result).toMatchObject({ id: 31, status: 'canceled' })
    expect(postMock).toHaveBeenCalledWith('/answer-jobs/31/cancel', undefined, {
      signal: undefined,
    })
  })

  it('updates and deletes a conversation through the protected endpoints', async () => {
    patchMock.mockResolvedValueOnce({
      data: { ...conversation, title: 'Renamed', status: 'archived' },
      status: 200,
    })
    deleteMock.mockResolvedValueOnce({ data: '', status: 204 })

    await expect(
      updateConversation(11, { title: 'Renamed', status: 'archived' }),
    ).resolves.toMatchObject({ id: 11, title: 'Renamed', status: 'archived' })
    await expect(deleteConversation(11)).resolves.toBeUndefined()

    expect(patchMock).toHaveBeenCalledWith(
      '/conversations/11',
      { title: 'Renamed', status: 'archived' },
      { signal: undefined },
    )
    expect(deleteMock).toHaveBeenCalledWith('/conversations/11', { signal: undefined })
  })

  it('rejects malformed update and delete responses', async () => {
    patchMock
      .mockResolvedValueOnce({ data: { ...conversation, id: 12 }, status: 200 })
      .mockResolvedValueOnce({ data: { id: 11 }, status: 200 })
    deleteMock.mockResolvedValueOnce({ data: {}, status: 200 })

    await expect(updateConversation(11, { title: 'Renamed' })).rejects.toThrow(
      new ApiError('invalid-response', '后端会话更新响应与请求 ID 不一致。'),
    )
    await expect(updateConversation(11, { status: 'archived' })).rejects.toThrow(
      new ApiError('invalid-response', '后端会话更新响应不符合约定。'),
    )
    await expect(deleteConversation(11)).rejects.toThrow(
      new ApiError('invalid-response', '后端会话删除响应不符合约定。'),
    )
  })

  it('rejects a fetched answer job whose ID differs from the request', async () => {
    getMock.mockResolvedValueOnce({ data: { ...answerJob, id: 32 }, status: 200 })

    await expect(getConversationAnswerJob(31)).rejects.toThrow(
      new ApiError('invalid-response', '后端回答任务响应与请求 ID 不一致。'),
    )
  })

  it('finds the latest answer job for a conversation and treats 204 as empty', async () => {
    getMock
      .mockResolvedValueOnce({ data: answerJob, status: 200 })
      .mockResolvedValueOnce({ data: '', status: 204 })

    await expect(getLatestConversationAnswerJob(11)).resolves.toMatchObject({
      id: 31,
      conversationId: 11,
      status: 'queued',
    })
    await expect(getLatestConversationAnswerJob(11)).resolves.toBeNull()

    expect(getMock).toHaveBeenNthCalledWith(1, '/conversations/11/answer-jobs/latest', {
      signal: undefined,
    })
    expect(getMock).toHaveBeenNthCalledWith(2, '/conversations/11/answer-jobs/latest', {
      signal: undefined,
    })
  })

  it('rejects a latest answer job that belongs to another conversation', async () => {
    getMock.mockResolvedValueOnce({
      data: { ...answerJob, conversation_id: 12 },
      status: 200,
    })

    await expect(getLatestConversationAnswerJob(11)).rejects.toThrow(
      new ApiError('invalid-response', '后端最近回答任务响应与请求会话不一致。'),
    )
  })

  it('maps a succeeded latest job with sources and token usage', async () => {
    getMock.mockResolvedValueOnce({
      data: succeededAnswerJob,
      status: 200,
    })

    await expect(getLatestConversationAnswerJob(11)).resolves.toMatchObject({
      status: 'succeeded',
      result: {
        sources: [{ chunkId: 501, documentId: 7 }],
        usage: { promptTokens: 100, completionTokens: 20, totalTokens: 120 },
      },
    })
  })

  it('rejects an answer start response for a different requested conversation', async () => {
    postMock.mockResolvedValueOnce({
      data: {
        conversation_id: 12,
        user_message: { ...message, conversation_id: 12 },
        assistant_message: { ...assistantMessage, conversation_id: 12 },
        answer_job: { ...answerJob, conversation_id: 12 },
      },
      status: 202,
    })

    await expect(
      queueConversationAnswer(11, {
        content: 'explain',
        clientMessageId: 'client-2',
        topK: 5,
        responseLanguage: 'auto',
      }),
    ).rejects.toThrow(new ApiError('invalid-response', '后端会话回答任务响应与请求会话不一致。'))
  })
})
