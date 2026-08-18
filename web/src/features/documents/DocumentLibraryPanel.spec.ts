import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DocumentPage, ResearchDocument } from '../../entities/document/model/document'
import { listDocuments } from './api/document-api'
import DocumentLibraryPanel from './ui/DocumentLibraryPanel.vue'

vi.mock('./api/document-api', () => ({
  listDocuments: vi.fn(),
}))

const listDocumentsMock = vi.mocked(listDocuments)

const existingDocument: ResearchDocument = {
  id: 42,
  title: 'Maglev control study',
  originalName: 'original-name.pdf',
  mimeType: 'application/pdf',
  sizeBytes: 2048,
  sha256: 'a'.repeat(64),
  status: 'uploaded',
  errorMessage: null,
  createdAt: new Date('2026-08-18T02:00:00Z'),
  updatedAt: new Date('2026-08-18T02:00:00Z'),
}

const documentPage: DocumentPage = {
  documents: [existingDocument],
  pagination: { page: 1, pageSize: 20, total: 1, totalPages: 1 },
}

beforeEach(() => {
  listDocumentsMock.mockReset()
  listDocumentsMock.mockResolvedValue(documentPage)
})

describe('DocumentLibraryPanel', () => {
  it('加载并展示当前用户的文档列表', async () => {
    const wrapper = mount(DocumentLibraryPanel)
    await flushPromises()

    expect(listDocumentsMock).toHaveBeenCalledWith(
      { page: 1, pageSize: 20 },
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('Maglev control study')
    expect(wrapper.text()).toContain('original-name.pdf')
    expect(wrapper.text()).toContain('等待解析')
    wrapper.unmount()
  })

  it('点击文档行时只向页面上报选中的文档 ID', async () => {
    const wrapper = mount(DocumentLibraryPanel)
    await flushPromises()

    await wrapper.get('.document-select-button').trigger('click')

    expect(wrapper.emitted('select')).toEqual([[42]])
    wrapper.unmount()
  })
})
