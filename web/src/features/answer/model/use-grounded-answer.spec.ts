import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { GroundedAnswer } from '../../../entities/answer/model/grounded-answer'
import { ApiError } from '../../../shared/api/api-error'
import { askGroundedQuestion } from '../api/answer-api'
import { useGroundedAnswer } from './use-grounded-answer'

vi.mock('../api/answer-api', () => ({ askGroundedQuestion: vi.fn() }))

const askGroundedQuestionMock = vi.mocked(askGroundedQuestion)

const answer: GroundedAnswer = {
  query: '问题',
  answer: '回答。[1]',
  responseLanguage: 'zh',
  sources: [
    {
      citation: 1,
      chunkId: 1,
      documentId: 2,
      chunkIndex: 0,
      title: null,
      originalName: 'source.pdf',
      pageStart: 1,
      pageEnd: 1,
      similarity: 0.9,
    },
  ],
  usage: { promptTokens: 10, completionTokens: 5, totalTokens: 15 },
}

beforeEach(() => askGroundedQuestionMock.mockReset())
afterEach(() => vi.useRealTimers())

describe('useGroundedAnswer', () => {
  it('区分有来源回答和无证据安全降级', async () => {
    const scope = effectScope()
    const model = scope.run(() => useGroundedAnswer())!
    askGroundedQuestionMock.mockResolvedValueOnce(answer)

    await model.ask({ query: '问题', topK: 5, responseLanguage: 'zh' })
    expect(model.state.value).toBe('success')
    expect(model.result.value).toEqual(answer)

    askGroundedQuestionMock.mockResolvedValueOnce({ ...answer, sources: [] })
    await model.ask({ query: '未知问题', topK: 5, responseLanguage: 'auto' })
    expect(model.state.value).toBe('insufficient-evidence')
    scope.stop()
  })

  it('把向量未就绪转换为可执行提示并保留请求编号', async () => {
    const scope = effectScope()
    const model = scope.run(() => useGroundedAnswer())!
    askGroundedQuestionMock.mockRejectedValueOnce(
      new ApiError('conflict', 'document embeddings are not ready', {
        status: 409,
        requestId: 'answer-409',
      }),
    )

    await model.ask({ query: '问题', documentId: 2, topK: 5, responseLanguage: 'auto' })

    expect(model.state.value).toBe('error')
    expect(model.errorMessage.value).toContain('“向量化”页面')
    expect(model.requestId.value).toBe('answer-409')
    expect(model.canRetry.value).toBe(true)
    scope.stop()
  })

  it('重试时复用最近一次参数', async () => {
    const scope = effectScope()
    const model = scope.run(() => useGroundedAnswer())!
    askGroundedQuestionMock
      .mockRejectedValueOnce(new ApiError('timeout', 'timeout'))
      .mockResolvedValueOnce(answer)
    const params = { query: '问题', documentId: 2, topK: 3, responseLanguage: 'zh' as const }

    await model.ask(params)
    await model.retry()

    expect(askGroundedQuestionMock).toHaveBeenNthCalledWith(2, params, expect.any(AbortSignal))
    expect(model.state.value).toBe('success')
    scope.stop()
  })

  it('问答容量满载时遵守 Retry-After，且不会自动或提前重放请求', async () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const model = scope.run(() => useGroundedAnswer())!
    askGroundedQuestionMock
      .mockRejectedValueOnce(
        new ApiError('server', 'busy', {
          status: 503,
          code: 'answer_capacity_exhausted',
          requestId: 'answer-capacity-1',
          retryAfterSeconds: 2,
        }),
      )
      .mockResolvedValueOnce(answer)

    await model.ask({ query: '问题', topK: 5, responseLanguage: 'zh' })

    expect(model.capacityFailure.value?.title).toContain('问答服务')
    expect(model.retryAvailable.value).toBe(true)
    expect(model.canRetry.value).toBe(false)
    expect(model.retryAfterSeconds.value).toBe(2)
    expect(model.requestId.value).toBe('answer-capacity-1')

    await model.retry()
    expect(askGroundedQuestionMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(2_000)
    expect(model.canRetry.value).toBe(true)
    expect(askGroundedQuestionMock).toHaveBeenCalledTimes(1)

    await model.retry()
    expect(askGroundedQuestionMock).toHaveBeenCalledTimes(2)
    expect(model.state.value).toBe('success')
    scope.stop()
  })

  it('把未注册的问答路由解释为功能未启用', async () => {
    const scope = effectScope()
    const model = scope.run(() => useGroundedAnswer())!
    askGroundedQuestionMock.mockRejectedValueOnce(
      new ApiError('not-found', '请求的资源不存在。', { status: 404 }),
    )

    await model.ask({ query: '问题', topK: 5, responseLanguage: 'auto' })

    expect(model.errorMessage.value).toContain('ANSWER_ENABLED')
    scope.stop()
  })
})
