import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { GroundedAnswer } from '../../entities/answer/model/grounded-answer'
import { ApiError } from '../../shared/api/api-error'
import { askGroundedQuestion } from './api/answer-api'
import GroundedAnswerPanel from './ui/GroundedAnswerPanel.vue'

vi.mock('./api/answer-api', () => ({ askGroundedQuestion: vi.fn() }))

const askGroundedQuestionMock = vi.mocked(askGroundedQuestion)

const answer: GroundedAnswer = {
  query: '如何抑制磁悬浮振动？',
  answer: '可以采用反馈控制。[1]',
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
}

beforeEach(() => askGroundedQuestionMock.mockReset())
afterEach(() => vi.useRealTimers())

function mountPanel(scopeIsValid = true) {
  return mount(GroundedAnswerPanel, {
    props: { scope: { kind: 'single', documentId: 20 }, scopeIsValid },
    global: {
      stubs: { RouterLink: { template: '<a><slot /></a>' } },
    },
  })
}

describe('GroundedAnswerPanel', () => {
  it('不会在挂载时调用模型，并提交单篇范围、语言和证据数量', async () => {
    askGroundedQuestionMock.mockResolvedValue(answer)
    const wrapper = mountPanel()

    expect(askGroundedQuestionMock).not.toHaveBeenCalled()
    await wrapper.get('#answer-query').setValue('  如何抑制磁悬浮振动？  ')
    await wrapper.get('select').setValue('zh')
    await wrapper.findAll('select')[1].setValue('3')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(askGroundedQuestionMock).toHaveBeenCalledWith(
      {
        query: '如何抑制磁悬浮振动？',
        documentId: 20,
        topK: 3,
        responseLanguage: 'zh',
      },
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('可以采用反馈控制。[1]')
    expect(wrapper.text()).toContain('磁悬浮系统稳定性研究')
    expect(wrapper.text()).toContain('第 3–4 页')
    expect(wrapper.text()).toContain('150')
    wrapper.unmount()
  })

  it('展示无证据降级且不伪造来源', async () => {
    askGroundedQuestionMock.mockResolvedValue({
      ...answer,
      answer: '现有文献中没有找到足够证据。',
      sources: [],
      usage: { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
    })
    const wrapper = mountPanel()
    await wrapper.get('#answer-query').setValue('未知问题')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('证据不足')
    expect(wrapper.text()).toContain('跳过了生成模型')
    expect(wrapper.find('.source-section').exists()).toBe(false)
    wrapper.unmount()
  })

  it('在向量未就绪时提供向量化入口和请求编号', async () => {
    askGroundedQuestionMock.mockRejectedValueOnce(
      new ApiError('conflict', 'document embeddings are not ready', {
        status: 409,
        requestId: 'answer-409',
      }),
    )
    const wrapper = mountPanel()
    await wrapper.get('#answer-query').setValue('问题')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('向量尚未就绪')
    expect(wrapper.text()).toContain('请求编号：answer-409')
    expect(wrapper.text()).toContain('查看向量化状态')
    wrapper.unmount()
  })

  it('问答容量满载时保留输入、展示倒计时并只开放手动重试', async () => {
    vi.useFakeTimers()
    askGroundedQuestionMock.mockRejectedValueOnce(
      new ApiError('server', 'busy', {
        status: 503,
        code: 'answer_capacity_exhausted',
        requestId: 'answer-capacity-ui-1',
        retryAfterSeconds: 2,
      }),
    )
    const wrapper = mountPanel()
    await wrapper.get('#answer-query').setValue('容量测试问题')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('.answer-state--capacity').text()).toContain('问答服务暂时繁忙')
    expect(wrapper.get('.answer-state--capacity').text()).toContain('2 秒')
    expect(wrapper.get('#answer-query').element).toHaveProperty('value', '容量测试问题')
    expect(wrapper.get('.primary-button').attributes('disabled')).toBeDefined()
    expect(wrapper.get('.error-actions button').attributes('disabled')).toBeDefined()

    await vi.advanceTimersByTimeAsync(2_000)
    expect(askGroundedQuestionMock).toHaveBeenCalledTimes(1)
    expect(wrapper.get('.primary-button').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('.error-actions button').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('失效范围不会发送问答请求', async () => {
    const wrapper = mountPanel(false)
    await wrapper.get('#answer-query').setValue('问题')
    expect(wrapper.get('.primary-button').attributes('disabled')).toBeDefined()
    expect(askGroundedQuestionMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
