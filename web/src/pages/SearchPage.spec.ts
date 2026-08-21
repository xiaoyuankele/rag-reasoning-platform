import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DocumentPage, ResearchDocument } from '../entities/document/model/document'
import type { KeywordSearchPage } from '../entities/search-result/model/search-result'
import { listDocuments } from '../features/documents/api/document-api'
import { searchKeywords } from '../features/search/api/search-keywords'
import SearchPage from './SearchPage.vue'

vi.mock('../features/documents/api/document-api', () => ({
  listDocuments: vi.fn(),
}))

vi.mock('../features/search/api/search-keywords', () => ({
  searchKeywords: vi.fn(),
}))

const listDocumentsMock = vi.mocked(listDocuments)
const searchKeywordsMock = vi.mocked(searchKeywords)

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

beforeEach(() => {
  listDocumentsMock.mockReset()
  listDocumentsMock.mockResolvedValue(documentPage)
  searchKeywordsMock.mockReset()
  searchKeywordsMock.mockResolvedValue(searchPage)
})

describe('SearchPage document scope integration', () => {
  it('切换单篇范围时保留关键词、清除旧页码并重新检索', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/search', name: 'search', component: SearchPage }],
    })
    await router.push('/search?q=bridge&page=2')
    await router.isReady()

    const wrapper = mount(SearchPage, { global: { plugins: [router] } })
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
})
