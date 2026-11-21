import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DocumentPage, ResearchDocument } from '../entities/document/model/document'
import { askGroundedQuestion } from '../features/answer/api/answer-api'
import { listDocuments } from '../features/documents/api/document-api'
import AnswerPage from './AnswerPage.vue'

vi.mock('../features/documents/api/document-api', () => ({ listDocuments: vi.fn() }))
vi.mock('../features/answer/api/answer-api', () => ({ askGroundedQuestion: vi.fn() }))

const listDocumentsMock = vi.mocked(listDocuments)
const askGroundedQuestionMock = vi.mocked(askGroundedQuestion)

const readyDocument: ResearchDocument = {
  id: 42,
  title: 'RAG architecture',
  originalName: 'rag.md',
  mimeType: 'text/markdown',
  sizeBytes: 1024,
  sha256: 'a'.repeat(64),
  status: 'ready',
  errorMessage: null,
  createdAt: new Date('2026-08-21T01:00:00Z'),
  updatedAt: new Date('2026-08-21T01:00:00Z'),
}

const documentPage: DocumentPage = {
  documents: [readyDocument],
  pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
}

beforeEach(() => {
  listDocumentsMock.mockReset()
  listDocumentsMock.mockResolvedValue(documentPage)
  askGroundedQuestionMock.mockReset()
})

describe('AnswerPage document scope integration', () => {
  it('切换单篇范围只更新 URL，不会自动调用远程问答', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/answer', name: 'answer', component: AnswerPage }],
    })
    await router.push('/answer')
    await router.isReady()

    const wrapper = mount(AnswerPage, { global: { plugins: [router] } })
    await flushPromises()
    await wrapper.get('#document-scope').setValue('document:42')
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({ document_id: '42' })
    expect(askGroundedQuestionMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
