import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'
import {
  cancelAgentRun,
  createAgentRun,
  getActiveAgentRun,
  getAgentRun,
  getLatestAgentRun,
  isAgentCapabilityUnavailable,
  mapAgentRunDto,
  mapCreateAgentRunResponse,
} from './agent-api'

vi.mock('../../../shared/api/http-client', () => ({
  httpClient: { get: vi.fn(), post: vi.fn() },
}))

const getMock = vi.mocked(httpClient.get)
const postMock = vi.mocked(httpClient.post)

const zeroUsage = { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 }

const queuedRunDto = {
  id: 701,
  conversation_id: 11,
  user_message_id: 201,
  assistant_message_id: 202,
  client_run_id: 'run-701',
  requested_document_id: 123,
  requested_response_language: 'auto',
  status: 'queued',
  step_count: 0,
  max_steps: 5,
  max_total_tokens: 48_000,
  cancelable: true,
  steps: [],
  result: null,
  error: null,
  token_usage: zeroUsage,
  created_at: '2026-11-16T10:00:00Z',
  updated_at: '2026-11-16T10:00:00Z',
  started_at: null,
  completed_at: null,
}

const searchStep = {
  id: 801,
  agent_run_id: 701,
  sequence_no: 1,
  kind: 'tool_call',
  status: 'succeeded',
  tool_name: 'search_documents',
  error: null,
  token_usage: { prompt_tokens: 620, completion_tokens: 54, total_tokens: 674 },
  created_at: '2026-11-16T10:00:01Z',
  started_at: '2026-11-16T10:00:01Z',
  completed_at: '2026-11-16T10:00:03Z',
}

const readStep = {
  id: 802,
  agent_run_id: 701,
  sequence_no: 2,
  kind: 'tool_call',
  status: 'succeeded',
  tool_name: 'read_document_evidence',
  error: null,
  token_usage: { prompt_tokens: 510, completion_tokens: 40, total_tokens: 550 },
  created_at: '2026-11-16T10:00:04Z',
  started_at: '2026-11-16T10:00:04Z',
  completed_at: '2026-11-16T10:00:06Z',
}

const finalStep = {
  id: 803,
  agent_run_id: 701,
  sequence_no: 3,
  kind: 'final',
  status: 'succeeded',
  tool_name: null,
  error: null,
  token_usage: { prompt_tokens: 900, completion_tokens: 210, total_tokens: 1110 },
  created_at: '2026-11-16T10:00:07Z',
  started_at: '2026-11-16T10:00:07Z',
  completed_at: '2026-11-16T10:00:12Z',
}

const succeededRunDto = {
  ...queuedRunDto,
  status: 'succeeded',
  step_count: 3,
  cancelable: false,
  steps: [searchStep, readStep, finalStep],
  result: {
    answer: '方案 A 更强调低延迟，方案 B 更强调故障恢复。[1]',
    response_language: 'zh',
    sources: [
      {
        citation: 1,
        evidence_ref: 'step-1:item-1',
        document_id: 123,
        chunk_id: 456,
        title: '系统设计',
        page_start: 12,
        page_end: 13,
        content_hash: 'a'.repeat(64),
      },
    ],
  },
  token_usage: { prompt_tokens: 2030, completion_tokens: 304, total_tokens: 2334 },
  updated_at: '2026-11-16T10:00:12Z',
  started_at: '2026-11-16T10:00:01Z',
  completed_at: '2026-11-16T10:00:12Z',
}

const userMessageDto = {
  id: 201,
  conversation_id: 11,
  sequence_no: 5,
  role: 'user',
  content: '比较两份文档。',
  status: 'completed',
  client_message_id: null,
  created_at: '2026-11-16T10:00:00Z',
  updated_at: '2026-11-16T10:00:00Z',
}

const assistantMessageDto = {
  id: 202,
  conversation_id: 11,
  sequence_no: 6,
  role: 'assistant',
  content: '正在生成回答…',
  status: 'generating',
  client_message_id: null,
  created_at: '2026-11-16T10:00:00Z',
  updated_at: '2026-11-16T10:00:00Z',
}

const createResponse = {
  conversation_id: 11,
  user_message: userMessageDto,
  assistant_message: assistantMessageDto,
  agent_run: queuedRunDto,
}

beforeEach(() => {
  getMock.mockReset()
  postMock.mockReset()
})

describe('Agent DTO mapping', () => {
  it('maps queued, recovered queued, processing, terminal, result, sources, and dates', () => {
    expect(mapAgentRunDto(queuedRunDto)).toMatchObject({
      id: 701,
      status: 'queued',
      requestedDocumentId: 123,
      tokenUsage: { totalTokens: 0 },
    })

    const pendingStep = {
      ...searchStep,
      status: 'pending',
      started_at: null,
      completed_at: null,
    }
    expect(
      mapAgentRunDto({
        ...queuedRunDto,
        step_count: 1,
        steps: [pendingStep],
        token_usage: pendingStep.token_usage,
        updated_at: '2026-11-16T10:00:01Z',
        started_at: '2026-11-16T10:00:01Z',
      }),
    ).toMatchObject({ status: 'queued', steps: [{ status: 'pending', startedAt: null }] })

    const runningStep = {
      ...searchStep,
      status: 'running',
      completed_at: null,
    }
    expect(
      mapAgentRunDto({
        ...queuedRunDto,
        status: 'processing',
        step_count: 1,
        cancelable: false,
        steps: [runningStep],
        token_usage: runningStep.token_usage,
        updated_at: '2026-11-16T10:00:01Z',
        started_at: '2026-11-16T10:00:01Z',
      }),
    ).toMatchObject({ status: 'processing', steps: [{ status: 'running' }] })

    expect(mapAgentRunDto(succeededRunDto)).toMatchObject({
      status: 'succeeded',
      stepCount: 3,
      result: {
        responseLanguage: 'zh',
        sources: [{ evidenceRef: 'step-1:item-1', contentHash: 'a'.repeat(64) }],
      },
      tokenUsage: { promptTokens: 2030, completionTokens: 304, totalTokens: 2334 },
    })
  })

  it('maps failed and canceled terminal snapshots', () => {
    const failedStep = {
      ...searchStep,
      status: 'failed',
      error: { code: 'agent_tool_failed', message: 'Agent tool execution failed' },
      token_usage: zeroUsage,
    }
    expect(
      mapAgentRunDto({
        ...queuedRunDto,
        status: 'failed',
        step_count: 1,
        cancelable: false,
        steps: [failedStep],
        error: { code: 'agent_tool_failed', message: 'Agent tool execution failed' },
        updated_at: '2026-11-16T10:00:03Z',
        started_at: '2026-11-16T10:00:01Z',
        completed_at: '2026-11-16T10:00:03Z',
      }),
    ).toMatchObject({ status: 'failed', error: { code: 'agent_tool_failed' } })
    expect(
      mapAgentRunDto({
        ...queuedRunDto,
        status: 'canceled',
        cancelable: false,
        updated_at: '2026-11-16T10:00:01Z',
        completed_at: '2026-11-16T10:00:01Z',
      }),
    ).toMatchObject({ status: 'canceled', startedAt: null })
  })

  it.each([
    ['unknown run status', { ...queuedRunDto, status: 'paused' }],
    [
      'unknown tool',
      { ...queuedRunDto, step_count: 1, steps: [{ ...searchStep, tool_name: 'shell' }] },
    ],
    [
      'sequence gap',
      { ...queuedRunDto, step_count: 1, steps: [{ ...searchStep, sequence_no: 2 }] },
    ],
    [
      'cross-run Step',
      { ...queuedRunDto, step_count: 1, steps: [{ ...searchStep, agent_run_id: 999 }] },
    ],
    [
      'token inequality',
      {
        ...queuedRunDto,
        step_count: 1,
        steps: [{ ...searchStep, token_usage: { ...searchStep.token_usage, total_tokens: 675 } }],
      },
    ],
    [
      'terminal active Step',
      {
        ...succeededRunDto,
        steps: [{ ...searchStep, status: 'running', completed_at: null }, readStep, finalStep],
      },
    ],
    ['unsafe ID', { ...queuedRunDto, id: Number.MAX_SAFE_INTEGER + 1 }],
    ['run internal field', { ...queuedRunDto, worker_id: 'worker-1' }],
    [
      'Step raw result',
      { ...queuedRunDto, step_count: 1, steps: [{ ...searchStep, result_json: {} }] },
    ],
    [
      'Step public updated_at',
      {
        ...queuedRunDto,
        step_count: 1,
        steps: [{ ...searchStep, updated_at: searchStep.created_at }],
      },
    ],
    [
      'Step raw arguments',
      { ...queuedRunDto, step_count: 1, steps: [{ ...searchStep, arguments: {} }] },
    ],
    ['usage internal field', { ...queuedRunDto, token_usage: { ...zeroUsage, worker: 1 } }],
    [
      'result internal field',
      { ...succeededRunDto, result: { ...succeededRunDto.result, provider_id: 'internal' } },
    ],
    [
      'error internal field',
      {
        ...queuedRunDto,
        status: 'failed',
        cancelable: false,
        error: {
          code: 'agent_execution_failed',
          message: 'Agent execution failed',
          stack: 'internal',
        },
        updated_at: '2026-11-16T10:00:01Z',
        started_at: '2026-11-16T10:00:00Z',
        completed_at: '2026-11-16T10:00:01Z',
      },
    ],
    [
      'source excerpt',
      {
        ...succeededRunDto,
        result: {
          ...succeededRunDto.result,
          sources: [{ ...succeededRunDto.result.sources[0], excerpt: 'internal text' }],
        },
      },
    ],
  ])('rejects malformed or polluted DTO: %s', (_name, dto) => {
    expect(() => mapAgentRunDto(dto)).toThrow(
      new ApiError('invalid-response', '后端 Agent 响应不符合约定。'),
    )
  })

  it('rejects invalid result, source, final, and aggregate invariants', () => {
    const invalidDtos = [
      { ...succeededRunDto, result: { ...succeededRunDto.result, response_language: 'auto' } },
      {
        ...succeededRunDto,
        result: {
          ...succeededRunDto.result,
          sources: [{ ...succeededRunDto.result.sources[0], citation: 2 }],
        },
      },
      {
        ...succeededRunDto,
        result: {
          ...succeededRunDto.result,
          sources: [{ ...succeededRunDto.result.sources[0], evidence_ref: 'step-2:item-1' }],
        },
      },
      {
        ...succeededRunDto,
        steps: [searchStep, readStep, { ...finalStep, tool_name: 'search_documents' }],
      },
      {
        ...succeededRunDto,
        token_usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
      },
      {
        ...succeededRunDto,
        steps: [searchStep, { ...readStep, id: searchStep.id }, finalStep],
      },
      { ...succeededRunDto, started_at: null },
      { ...succeededRunDto, updated_at: '2026-11-16T10:00:11Z' },
      { ...queuedRunDto, completed_at: '2026-11-16T10:00:01Z' },
      {
        ...queuedRunDto,
        status: 'failed',
        cancelable: false,
        error: { code: 'agent_execution_failed', message: 'provider secret' },
        started_at: '2026-11-16T10:00:00Z',
        completed_at: '2026-11-16T10:00:01Z',
      },
      {
        ...succeededRunDto,
        result: {
          ...succeededRunDto.result,
          sources: [
            {
              ...succeededRunDto.result.sources[0],
              content_hash: { toString: null, valueOf: null },
            },
          ],
        },
      },
    ]
    for (const dto of invalidDtos) {
      expect(() => mapAgentRunDto(dto)).toThrow(ApiError)
    }
  })

  it('validates create message and Run associations', () => {
    const params = {
      content: '比较两份文档。',
      clientRunId: ' run-701 ',
      documentId: 123,
      responseLanguage: 'auto' as const,
    }
    expect(mapCreateAgentRunResponse(createResponse, 11, params)).toMatchObject({
      conversationId: 11,
      userMessage: { id: 201, role: 'user' },
      assistantMessage: { id: 202, status: 'generating' },
      agentRun: { id: 701 },
    })
    expect(() =>
      mapCreateAgentRunResponse(
        { ...createResponse, agent_run: { ...queuedRunDto, conversation_id: 12 } },
        11,
        params,
      ),
    ).toThrow(new ApiError('invalid-response', '后端 Agent 创建响应关联不符合约定。'))
    expect(() =>
      mapCreateAgentRunResponse(
        { ...createResponse, user_message: { ...userMessageDto, owner_user_id: 9 } },
        11,
        params,
      ),
    ).toThrow(ApiError)
  })
})

describe('Agent API calls', () => {
  it('creates a Run with the frozen request and association response', async () => {
    postMock.mockResolvedValue({ status: 202, data: createResponse })
    await expect(
      createAgentRun(11, {
        content: '比较两份文档。',
        clientRunId: 'run-701',
        documentId: 123,
        responseLanguage: 'auto',
      }),
    ).resolves.toMatchObject({ agentRun: { id: 701, status: 'queued' } })
    expect(postMock).toHaveBeenCalledWith(
      '/conversations/11/agent-runs',
      {
        content: '比较两份文档。',
        client_run_id: 'run-701',
        document_id: 123,
        response_language: 'auto',
      },
      { signal: undefined },
    )
  })

  it('accepts a terminal idempotent replay only with its completed assistant message', async () => {
    postMock.mockResolvedValueOnce({
      status: 200,
      data: {
        ...createResponse,
        assistant_message: {
          ...assistantMessageDto,
          content: succeededRunDto.result.answer,
          status: 'completed',
        },
        agent_run: succeededRunDto,
      },
    })
    await expect(
      createAgentRun(11, {
        content: '比较两份文档。',
        clientRunId: 'run-701',
        documentId: 123,
        responseLanguage: 'auto',
      }),
    ).resolves.toMatchObject({ agentRun: { status: 'succeeded' } })

    postMock.mockResolvedValueOnce({
      status: 202,
      data: {
        ...createResponse,
        assistant_message: {
          ...assistantMessageDto,
          content: succeededRunDto.result.answer,
          status: 'completed',
        },
        agent_run: succeededRunDto,
      },
    })
    await expect(
      createAgentRun(11, {
        content: '比较两份文档。',
        clientRunId: 'run-701',
        documentId: 123,
        responseLanguage: 'auto',
      }),
    ).rejects.toThrow(ApiError)
  })

  it('gets, discovers, and cancels Run envelopes with path association checks', async () => {
    getMock
      .mockResolvedValueOnce({ status: 200, data: { agent_run: queuedRunDto } })
      .mockResolvedValueOnce({ status: 200, data: { agent_run: queuedRunDto } })
      .mockResolvedValueOnce({ status: 200, data: { agent_run: succeededRunDto } })
      .mockResolvedValueOnce({ status: 204, data: '' })
      .mockResolvedValueOnce({ status: 204, data: '' })
    postMock.mockResolvedValueOnce({
      status: 200,
      data: {
        agent_run: {
          ...queuedRunDto,
          status: 'canceled',
          cancelable: false,
          updated_at: '2026-11-16T10:00:01Z',
          completed_at: '2026-11-16T10:00:01Z',
        },
      },
    })

    await expect(getAgentRun(701)).resolves.toMatchObject({ id: 701 })
    await expect(getActiveAgentRun(11)).resolves.toMatchObject({ status: 'queued' })
    await expect(getLatestAgentRun(11)).resolves.toMatchObject({ status: 'succeeded' })
    await expect(getActiveAgentRun(11)).resolves.toBeNull()
    await expect(getLatestAgentRun(11)).resolves.toBeNull()
    await expect(cancelAgentRun(701)).resolves.toMatchObject({ status: 'canceled' })

    expect(getMock).toHaveBeenNthCalledWith(1, '/agent-runs/701', { signal: undefined })
    expect(getMock).toHaveBeenNthCalledWith(2, '/conversations/11/agent-runs/active', {
      signal: undefined,
    })
    expect(getMock).toHaveBeenNthCalledWith(3, '/conversations/11/agent-runs/latest', {
      signal: undefined,
    })
    expect(postMock).toHaveBeenCalledWith('/agent-runs/701/cancel', undefined, {
      signal: undefined,
    })
  })

  it('rejects endpoint-specific IDs, active state, cancel state, and success status codes', async () => {
    getMock
      .mockResolvedValueOnce({ status: 200, data: { agent_run: { ...queuedRunDto, id: 702 } } })
      .mockResolvedValueOnce({ status: 200, data: { agent_run: succeededRunDto } })
      .mockResolvedValueOnce({
        status: 200,
        data: { agent_run: { ...queuedRunDto, conversation_id: 12 } },
      })
      .mockResolvedValueOnce({ status: 201, data: { agent_run: queuedRunDto } })
    postMock.mockResolvedValueOnce({ status: 200, data: { agent_run: queuedRunDto } })

    await expect(getAgentRun(701)).rejects.toThrow(ApiError)
    await expect(getActiveAgentRun(11)).rejects.toThrow(ApiError)
    await expect(getLatestAgentRun(11)).rejects.toThrow(ApiError)
    await expect(getAgentRun(701)).rejects.toThrow(ApiError)
    await expect(cancelAgentRun(701)).rejects.toThrow(ApiError)
  })

  it('rejects polluted envelopes before reading their Run', async () => {
    getMock.mockResolvedValueOnce({
      status: 200,
      data: { agent_run: queuedRunDto, worker_id: 'internal' },
    })
    await expect(getAgentRun(701)).rejects.toThrow(ApiError)
  })

  it('distinguishes a disabled generic 404 from stable resource 404 codes', () => {
    expect(
      isAgentCapabilityUnavailable(new ApiError('not-found', 'route missing', { status: 404 })),
    ).toBe(true)
    expect(
      isAgentCapabilityUnavailable(
        new ApiError('not-found', 'conversation missing', {
          status: 404,
          code: 'conversation_not_found',
        }),
      ),
    ).toBe(false)
    expect(
      isAgentCapabilityUnavailable(
        new ApiError('not-found', 'run missing', { status: 404, code: 'agent_run_not_found' }),
      ),
    ).toBe(false)
  })
})
