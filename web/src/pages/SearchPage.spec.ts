import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DocumentPage, ResearchDocument } from '../entities/document/model/document'
import type {
  KeywordSearchPage,
  SemanticSearchResult,
} from '../entities/search-result/model/search-result'
import { listDocuments } from '../features/documents/api/document-api'
import { useAuthStore } from '../features/auth/store/auth-store'
import { searchKeywords } from '../features/search/api/search-keywords'
import { searchSemantically } from '../features/search/api/search-semantically'
import SearchPage from './SearchPage.vue'

vi.mock('../features/documents/api/document-api', () => ({
  listDocuments: vi.fn(),
}))

vi.mock('../features/search/api/search-keywords', () => ({
  searchKeywords: vi.fn(),
}))

vi.mock('../features/search/api/search-semantically', () => ({
  searchSemantically: vi.fn(),
}))

const listDocumentsMock = vi.mocked(listDocuments)
const searchKeywordsMock = vi.mocked(searchKeywords)
const searchSemanticallyMock = vi.mocked(searchSemantically)

const readyDocument: ResearchDocument = {
  id: 42,
  title: 'RAG architecture',
  originalName: 'rag.md',
  mimeType: 'text/markdown',
  sizeBytes: 1024,
  sha256: 'a'.repeat(64),
  status: 'ready',
  errorMessage: null,
  createdAt: new Date('2026-08-19T01:00:00Z'),
  updatedAt: new Date('2026-08-19T01:00:00Z'),
}

const documentPage: DocumentPage = {
  documents: [readyDocument],
  pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
}

const searchPage: KeywordSearchPage = {
  query: 'bridge',
  terms: [],
  operator: null,
  within: null,
  results: [],
  pagination: { page: 1, pageSize: 10, total: 0, totalPages: 0 },
}

const semanticResult: SemanticSearchResult = {
  query: 'bridge stability',
  hits: [],
}

let pinia: Pinia

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  pinia = createPinia()
  setActivePinia(pinia)
  useAuthStore(pinia).$patch({
    status: 'authenticated',
    sessionExpiresAt: null,
    user: {
      id: 17,
      email: 'learner@example.com',
      phone: null,
      displayName: 'learner',
      status: 'active',
      createdAt: '2026-08-17T08:04:16Z',
    },
  })
  listDocumentsMock.mockReset()
  listDocumentsMock.mockResolvedValue(documentPage)
  searchKeywordsMock.mockReset()
  searchKeywordsMock.mockResolvedValue(searchPage)
  searchSemanticallyMock.mockReset()
  searchSemanticallyMock.mockResolvedValue(semanticResult)
})

describe('SearchPage document scope integration', () => {
  it('切换单篇范围时保留关键词、清除旧页码并重新检索', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/search', name: 'search', component: SearchPage }],
    })
    await router.push('/search?q=bridge&page=2')
    await router.isReady()

    const wrapper = mount(SearchPage, { global: { plugins: [pinia, router] } })
    await flushPromises()

    await wrapper.get('#document-scope').setValue('document:42')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({ q: 'bridge', document_id: '42' })
    expect(searchKeywordsMock).toHaveBeenLastCalledWith(
      {
        mode: 'phrase',
        query: 'bridge',
        documentId: 42,
        page: 1,
        pageSize: 10,
      },
      expect.any(AbortSignal),
    )
    wrapper.unmount()
  })

  it('显式切换语义模式不会自动调用模型，提交时复用当前文档范围', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/search', name: 'search', component: SearchPage }],
    })
    await router.push('/search?document_id=42')
    await router.isReady()

    const wrapper = mount(SearchPage, { global: { plugins: [pinia, router] } })
    await flushPromises()

    await wrapper.get('.retrieval-mode-selector button:last-child').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({ mode: 'semantic', document_id: '42' })
    expect(searchSemanticallyMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('本操作会调用远程 Embedding 模型')

    await wrapper.get('#semantic-query').setValue('bridge stability')
    await wrapper.get('.semantic-search-form').trigger('submit')
    await flushPromises()

    expect(searchSemanticallyMock).toHaveBeenCalledWith(
      { query: 'bridge stability', documentId: 42, topK: 5 },
      expect.any(AbortSignal),
    )
    wrapper.unmount()
  })
})
