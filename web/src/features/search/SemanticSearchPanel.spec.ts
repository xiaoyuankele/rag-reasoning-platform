import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SemanticSearchResult } from '../../entities/search-result/model/search-result'
import { ApiError } from '../../shared/api/api-error'
import { searchSemantically } from './api/search-semantically'
import SemanticSearchPanel from './ui/SemanticSearchPanel.vue'

vi.mock('./api/search-semantically', () => ({ searchSemantically: vi.fn() }))

const searchSemanticallyMock = vi.mocked(searchSemantically)
const result: SemanticSearchResult = {
  query: '如何提高悬浮稳定性？',
  hits: [
    {
      chunkId: 11,
      documentId: 7,
      chunkIndex: 2,
      title: 'Maglev stability study',
      originalName: 'maglev.pdf',
      mimeType: 'application/pdf',
      content: 'Feedback control can improve suspension stability.',
      pageStart: 3,
      pageEnd: 4,
      similarity: 0.91,
    },
  ],
}

beforeEach(() => {
  sessionStorage.clear()
  searchSemanticallyMock.mockReset()
})
afterEach(() => vi.useRealTimers())

function mountPanel(scopeIsValid = true) {
  return mount(SemanticSearchPanel, {
    props: { cacheOwnerUserId: 17, scope: { kind: 'single', documentId: 7 }, scopeIsValid },
    global: {
      stubs: { RouterLink: { template: '<a><slot /></a>' } },
    },
  })
}

describe('SemanticSearchPanel', () => {
  it('挂载和输入不会调用模型，显式提交后展示来源、页码和相似度', async () => {
    searchSemanticallyMock.mockResolvedValue(result)
    const wrapper = mountPanel()

    expect(searchSemanticallyMock).not.toHaveBeenCalled()
    await wrapper.get('#semantic-query').setValue('  如何提高悬浮稳定性？  ')
    expect(searchSemanticallyMock).not.toHaveBeenCalled()
    await wrapper.get('select').setValue('10')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(searchSemanticallyMock).toHaveBeenCalledWith(
      { query: '如何提高悬浮稳定性？', documentId: 7, topK: 10 },
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('Maglev stability study')
    expect(wrapper.text()).toContain('第 3–4 页')
    expect(wrapper.text()).toContain('相似度 91%')
    expect(sessionStorage.length).toBe(0)

    await wrapper.get('.retention-option input').setValue(true)
    expect(sessionStorage.length).toBe(1)
    await wrapper.get('.retention-option input').setValue(false)
    expect(sessionStorage.length).toBe(0)
    wrapper.unmount()
  })

  it('409 时提供向量化入口', async () => {
    searchSemanticallyMock.mockRejectedValueOnce(
      new ApiError('conflict', 'document embeddings are not ready', {
        status: 409,
        requestId: 'semantic-ui-409',
      }),
    )
    const wrapper = mountPanel()
    await wrapper.get('#semantic-query').setValue('问题')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('向量尚未就绪')
    expect(wrapper.text()).toContain('查看向量化状态')
    expect(wrapper.text()).toContain('请求编号：semantic-ui-409')
    wrapper.unmount()
  })

  it('容量等待时保留输入、显示倒计时并禁止重复提交', async () => {
    vi.useFakeTimers()
    searchSemanticallyMock.mockRejectedValueOnce(
      new ApiError('server', 'busy', {
        status: 503,
        code: 'embedding_provider_capacity_exhausted',
        requestId: 'semantic-capacity-ui-1',
        retryAfterSeconds: 2,
      }),
    )
    const wrapper = mountPanel()
    await wrapper.get('#semantic-query').setValue('容量测试')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('.state-card--capacity').text()).toContain('在线向量服务暂时繁忙')
    expect(wrapper.get('.state-card--capacity').text()).toContain('2 秒')
    expect(wrapper.get('#semantic-query').element).toHaveProperty('value', '容量测试')
    expect(wrapper.get('.primary-button').attributes('disabled')).toBeDefined()

    await vi.advanceTimersByTimeAsync(2_000)
    expect(searchSemanticallyMock).toHaveBeenCalledTimes(1)
    expect(wrapper.get('.primary-button').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('无效文档范围不会调用接口', async () => {
    const wrapper = mountPanel(false)
    await wrapper.get('#semantic-query').setValue('问题')

    expect(wrapper.get('.primary-button').attributes('disabled')).toBeDefined()
    expect(searchSemanticallyMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('刷新恢复同一用户和范围的最后结果，不会再次调用远程模型', async () => {
    searchSemanticallyMock.mockResolvedValueOnce(result)
    const firstWrapper = mountPanel()
    await firstWrapper.get('.retention-option input').setValue(true)
    await firstWrapper.get('#semantic-query').setValue('如何提高悬浮稳定性？')
    await firstWrapper.get('select').setValue('10')
    await firstWrapper.get('form').trigger('submit')
    await flushPromises()
    firstWrapper.unmount()

    searchSemanticallyMock.mockClear()
    const restoredWrapper = mountPanel()

    expect(searchSemanticallyMock).not.toHaveBeenCalled()
    expect(restoredWrapper.get('#semantic-query').element).toHaveProperty(
      'value',
      '如何提高悬浮稳定性？',
    )
    expect(restoredWrapper.get('select').element).toHaveProperty('value', '10')
    expect(restoredWrapper.text()).toContain('Maglev stability study')
    expect(restoredWrapper.get('.retention-option input').element).toHaveProperty('checked', true)

    await restoredWrapper.get('.retention-option input').setValue(false)
    expect(sessionStorage.length).toBe(0)
    restoredWrapper.unmount()
  })
})
