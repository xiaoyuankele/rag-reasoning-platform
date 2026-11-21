import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'
import { askGroundedQuestion, mapAnswerResponse } from './answer-api'

vi.mock('../../../shared/api/http-client', () => ({
  httpClient: { post: vi.fn() },
}))

const postMock = vi.mocked(httpClient.post)

const validResponse = {
  query: '如何抑制磁悬浮振动？',
  answer: '可使用反馈控制。[1]',
  response_language: 'zh',
  sources: [
    {
      citation: 1,
      chunk_id: 101,
      document_id: 20,
      chunk_index: 2,
      title: '磁悬浮系统稳定性研究',
      original_name: 'maglev.pdf',
      page_start: 3,
      page_end: 4,
      similarity: 0.93,
    },
  ],
  usage: { prompt_tokens: 120, completion_tokens: 30, total_tokens: 150 },
}

beforeEach(() => postMock.mockReset())

describe('mapAnswerResponse', () => {
  it('把后端回答、来源和 Token 用量转换为前端模型', () => {
    expect(mapAnswerResponse(validResponse)).toEqual({
      query: '如何抑制磁悬浮振动？',
      answer: '可使用反馈控制。[1]',
      responseLanguage: 'zh',
      sources: [
        {
          citation: 1,
          chunkId: 101,
          documentId: 20,
          chunkIndex: 2,
          title: '磁悬浮系统稳定性研究',
          originalName: 'maglev.pdf',
          pageStart: 3,
          pageEnd: 4,
          similarity: 0.93,
        },
      ],
      usage: { promptTokens: 120, completionTokens: 30, totalTokens: 150 },
    })
  })

  it('接受无证据的安全降级响应', () => {
    expect(
      mapAnswerResponse({
        ...validResponse,
        answer: '现有文献中没有找到足够证据。',
        sources: [],
        usage: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
      }).sources,
    ).toEqual([])
  })

  it('拒绝跳号引用和不一致 Token 合计', () => {
    expect(() =>
      mapAnswerResponse({
        ...validResponse,
        sources: [{ ...validResponse.sources[0], citation: 2 }],
      }),
    ).toThrow(new ApiError('invalid-response', '后端问答响应不符合约定。'))
    expect(() =>
      mapAnswerResponse({
        ...validResponse,
        usage: { prompt_tokens: 120, completion_tokens: 30, total_tokens: 149 },
      }),
    ).toThrow(new ApiError('invalid-response', '后端问答响应不符合约定。'))
  })
})

describe('askGroundedQuestion', () => {
  it('使用正式 DTO 和适合远程生成的超时发起请求', async () => {
    postMock.mockResolvedValue({ data: validResponse })
    const controller = new AbortController()

    await askGroundedQuestion(
      {
        query: '如何抑制磁悬浮振动？',
        documentId: 20,
        topK: 5,
        responseLanguage: 'zh',
      },
      controller.signal,
    )

    expect(postMock).toHaveBeenCalledWith(
      '/answers',
      {
        query: '如何抑制磁悬浮振动？',
        document_id: 20,
        top_k: 5,
        response_language: 'zh',
      },
      { signal: controller.signal, timeout: 70_000 },
    )
  })
})
