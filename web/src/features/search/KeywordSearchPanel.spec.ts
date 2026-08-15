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
    const { router, wrapper } = await mountPanel()

    await wrapper.get('#keyword-query').setValue('bridge')
    await wrapper.get('#document-id').setValue('7')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({
      q: 'bridge',
      document_id: '7',
    })
    expect(searchKeywordsMock).toHaveBeenCalledWith(
      {
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
})
