import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { KeywordSearchPage } from '../../entities/search-result/model/search-result'
import { ApiError } from '../../shared/api/api-error'
import { searchKeywords } from './api/search-keywords'
import KeywordSearchPanel from './ui/KeywordSearchPanel.vue'

vi.mock('./api/search-keywords', () => ({
  searchKeywords: vi.fn(),
}))

const searchKeywordsMock = vi.mocked(searchKeywords)

const firstPage: KeywordSearchPage = {
  query: 'bridge',
  terms: [],
  operator: null,
  within: null,
  results: [
    {
      chunkId: 11,
      documentId: 7,
      chunkIndex: 2,
      title: 'Bridge vibration study',
      originalName: 'bridge-study.pdf',
      mimeType: 'application/pdf',
      content: 'bridge vibration analysis',
      pageStart: 3,
      pageEnd: 4,
    },
  ],
  pagination: {
    page: 1,
    pageSize: 10,
    total: 11,
    totalPages: 2,
  },
}

async function mountPanel(initialPath = '/search') {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: '/search',
        name: 'search',
        component: { template: '<div />' },
      },
    ],
  })

  await router.push(initialPath)
  await router.isReady()

  const wrapper = mount(KeywordSearchPanel, {
    global: { plugins: [router] },
  })
  await flushPromises()

  return { router, wrapper }
}

beforeEach(() => {
  searchKeywordsMock.mockReset()
})

describe('KeywordSearchPanel', () => {
  it('提交关键词后同步 URL 并展示来源结果', async () => {
    searchKeywordsMock.mockResolvedValue(firstPage)
    const { router, wrapper } = await mountPanel('/search?document_id=7')

    await wrapper.get('#keyword-query').setValue('bridge')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({
      q: 'bridge',
      document_id: '7',
    })
    expect(searchKeywordsMock).toHaveBeenCalledWith(
      {
        mode: 'phrase',
        query: 'bridge',
        documentId: 7,
        page: 1,
        pageSize: 10,
      },
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('Bridge vibration study')
    expect(wrapper.text()).toContain('第 3–4 页')
    expect(wrapper.text()).toContain('共 11 个文本块')
    expect(wrapper.get('mark').text()).toBe('bridge')
    wrapper.unmount()
  })

  it('全部文档范围不向接口发送 documentId', async () => {
    searchKeywordsMock.mockResolvedValue(firstPage)
    const { wrapper } = await mountPanel('/search?q=bridge')

    expect(searchKeywordsMock).toHaveBeenCalledWith(
      {
        mode: 'phrase',
        query: 'bridge',
        documentId: undefined,
        page: 1,
        pageSize: 10,
      },
      expect.any(AbortSignal),
    )
    wrapper.unmount()
  })

  it('无效 URL 范围不会发送检索请求', async () => {
    const { wrapper } = await mountPanel('/search?q=bridge&document_id=abc')

    expect(searchKeywordsMock).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toContain('URL 中的文档范围无效')
    wrapper.unmount()
  })

  it('分页按钮把页码写入 URL 并请求下一页', async () => {
    searchKeywordsMock.mockResolvedValueOnce(firstPage).mockResolvedValueOnce({
      ...firstPage,
      results: [{ ...firstPage.results[0]!, chunkId: 12 }],
      pagination: { ...firstPage.pagination, page: 2 },
    })
    const { router, wrapper } = await mountPanel('/search?q=bridge')

    await wrapper.get('.pagination button:last-child').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query.page).toBe('2')
    expect(searchKeywordsMock).toHaveBeenLastCalledWith(
      expect.objectContaining({ page: 2 }),
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('第 2 / 2 页')
    wrapper.unmount()
  })

  it('展示安全错误并允许重新检索', async () => {
    searchKeywordsMock.mockRejectedValueOnce(
      new ApiError('network', '无法连接后端服务，请确认服务已经启动。'),
    )
    searchKeywordsMock.mockResolvedValueOnce(firstPage)
    const { wrapper } = await mountPanel('/search?q=bridge')

    expect(wrapper.get('[role="alert"]').text()).toContain('无法连接后端服务')

    await wrapper.get('.state-card--error button').trigger('click')
    await flushPromises()

    expect(searchKeywordsMock).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('Bridge vibration study')
    wrapper.unmount()
  })

  it('明确区分后端零结果与连接失败', async () => {
    searchKeywordsMock.mockResolvedValue({
      query: 'maglev vibration',
      terms: [],
      operator: null,
      within: null,
      results: [],
      pagination: {
        page: 1,
        pageSize: 10,
        total: 0,
        totalPages: 0,
      },
    })
    const { wrapper } = await mountPanel('/search?q=maglev%20vibration')

    expect(searchKeywordsMock).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('后端检索已完成')
    expect(wrapper.text()).toContain('连续完整短语匹配')
    expect(wrapper.text()).not.toContain('检索没有完成')
    wrapper.unmount()
  })

  it('从 URL 恢复同一文本块多关键词检索并高亮全部关键词', async () => {
    searchKeywordsMock.mockResolvedValue({
      ...firstPage,
      query: '',
      terms: ['maglev', 'vibration'],
      operator: 'all',
      within: 'chunk',
      results: [
        {
          ...firstPage.results[0]!,
          content: 'maglev vehicle vibration response',
        },
      ],
    })

    const { wrapper } = await mountPanel(
      '/search?term=maglev&term=vibration&operator=all&within=chunk&document_id=7',
    )

    expect(searchKeywordsMock).toHaveBeenCalledWith(
      {
        mode: 'terms',
        terms: ['maglev', 'vibration'],
        operator: 'all',
        within: 'chunk',
        documentId: 7,
        page: 1,
        pageSize: 10,
      },
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('同一文本块同时包含全部关键词')
    expect(wrapper.findAll('mark').map((mark) => mark.text())).toEqual(['maglev', 'vibration'])
    wrapper.unmount()
  })

  it('添加多个关键词后把重复 term、operator 和 within 写入 URL', async () => {
    searchKeywordsMock.mockResolvedValue({
      ...firstPage,
      query: '',
      terms: ['maglev', 'vibration'],
      operator: 'any',
      within: 'chunk',
    })
    const { router, wrapper } = await mountPanel('/search')

    await wrapper.get('input[value="terms"]').setValue(true)
    await wrapper.get('#keyword-term-input').setValue('maglev，vibration')
    await wrapper.get('.operator-field select').setValue('any')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({
      term: ['maglev', 'vibration'],
      operator: 'any',
      within: 'chunk',
    })
    expect(searchKeywordsMock).toHaveBeenLastCalledWith(
      expect.objectContaining({
        mode: 'terms',
        terms: ['maglev', 'vibration'],
        operator: 'any',
        within: 'chunk',
      }),
      expect.any(AbortSignal),
    )
    wrapper.unmount()
  })
})
