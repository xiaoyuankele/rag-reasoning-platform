import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DocumentPage, ResearchDocument } from '../../entities/document/model/document'
import { ApiError } from '../../shared/api/api-error'
import { listDocuments, uploadDocument } from './api/document-api'
import DocumentLibraryPanel from './ui/DocumentLibraryPanel.vue'

vi.mock('./api/document-api', () => ({
  listDocuments: vi.fn(),
  uploadDocument: vi.fn(),
}))

const listDocumentsMock = vi.mocked(listDocuments)
const uploadDocumentMock = vi.mocked(uploadDocument)

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

async function selectFile(wrapper: ReturnType<typeof mount>, file: File): Promise<void> {
  const input = wrapper.get<HTMLInputElement>('#document-file')
  Object.defineProperty(input.element, 'files', {
    configurable: true,
    value: [file],
  })
  await input.trigger('change')
}

beforeEach(() => {
  listDocumentsMock.mockReset()
  uploadDocumentMock.mockReset()
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

  it('把改名后的相同内容展示为已有记录，而不是上传失败', async () => {
    uploadDocumentMock.mockResolvedValue({ document: existingDocument, duplicate: true })
    const wrapper = mount(DocumentLibraryPanel)
    await flushPromises()

    const renamedFile = new File(['same bytes'], 'renamed-copy.pdf', {
      type: 'application/pdf',
    })
    await selectFile(wrapper, renamedFile)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(uploadDocumentMock).toHaveBeenCalledWith(renamedFile, expect.any(AbortSignal))
    expect(listDocumentsMock).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('该内容已存在')
    expect(wrapper.text()).toContain('renamed-copy.pdf')
    expect(wrapper.text()).toContain('original-name.pdf')
    expect(wrapper.text()).toContain('原记录 #42')
    expect(wrapper.find('.upload-notice--error').exists()).toBe(false)
    wrapper.unmount()
  })

  it('把不支持的文件响应转换为中文安全提示并保留请求编号', async () => {
    uploadDocumentMock.mockRejectedValue(
      new ApiError('client', 'file type must be supported', {
        status: 415,
        requestId: 'request-upload-1',
      }),
    )
    const wrapper = mount(DocumentLibraryPanel)
    await flushPromises()

    await selectFile(wrapper, new File(['binary'], 'sample.bin'))
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('.upload-notice--error').text()).toContain(
      '文件内容不受支持，请选择有效的 PDF、Markdown 或纯文本文件。',
    )
    expect(wrapper.text()).toContain('请求编号：request-upload-1')
    wrapper.unmount()
  })
})
