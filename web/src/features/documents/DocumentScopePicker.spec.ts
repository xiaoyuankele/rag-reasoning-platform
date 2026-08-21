import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DocumentPage, ResearchDocument } from '../../entities/document/model/document'
import { ApiError } from '../../shared/api/api-error'
import { listDocuments } from './api/document-api'
import DocumentScopePicker from './ui/DocumentScopePicker.vue'

vi.mock('./api/document-api', () => ({
  listDocuments: vi.fn(),
}))

const listDocumentsMock = vi.mocked(listDocuments)

function createDocument(id: number, status: ResearchDocument['status']): ResearchDocument {
  return {
    id,
    title: id === 42 ? 'Maglev study' : null,
    originalName: `document-${id}.pdf`,
    mimeType: 'application/pdf',
    sizeBytes: 2048,
    sha256: id.toString(16).padStart(64, 'a').slice(-64),
    status,
    errorMessage: null,
    createdAt: new Date('2026-08-19T01:00:00Z'),
    updatedAt: new Date('2026-08-19T01:00:00Z'),
  }
}

function createPage(documents: ResearchDocument[], page: number, totalPages: number): DocumentPage {
  return {
    documents,
    pagination: { page, pageSize: 100, total: documents.length, totalPages },
  }
}

beforeEach(() => {
  listDocumentsMock.mockReset()
})

describe('DocumentScopePicker', () => {
  it('读取全部分页但只展示 ready 文档，并上报单篇范围', async () => {
    listDocumentsMock
      .mockResolvedValueOnce(
        createPage([createDocument(42, 'ready'), createDocument(43, 'processing')], 1, 2),
      )
      .mockResolvedValueOnce(createPage([createDocument(51, 'ready')], 2, 2))

    const wrapper = mount(DocumentScopePicker, {
      props: { modelValue: { kind: 'all' } },
    })
    await flushPromises()

    expect(listDocumentsMock).toHaveBeenNthCalledWith(
      1,
      { page: 1, pageSize: 100 },
      expect.any(AbortSignal),
    )
    expect(listDocumentsMock).toHaveBeenNthCalledWith(
      2,
      { page: 2, pageSize: 100 },
      expect.any(AbortSignal),
    )
    expect(wrapper.findAll('option').map((option) => option.text())).toEqual([
      '全部可检索文档',
      'Maglev study · document-42.pdf · #42',
      'document-51.pdf · #51',
    ])

    await wrapper.get('select').setValue('document:42')
    expect(wrapper.emitted('update:modelValue')).toEqual([[{ kind: 'single', documentId: 42 }]])
    wrapper.unmount()
  })

  it('标记失效的单篇范围并允许回到全部文档', async () => {
    listDocumentsMock.mockResolvedValue(createPage([], 1, 0))
    const wrapper = mount(DocumentScopePicker, {
      props: { modelValue: { kind: 'single', documentId: 99 } },
    })
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('文档 #99 当前不可用于检索或问答')
    await wrapper.get('[role="alert"] button').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[{ kind: 'all' }]])
    wrapper.unmount()
  })

  it('列表失败时保留全部范围并提供安全错误和重试', async () => {
    listDocumentsMock
      .mockRejectedValueOnce(
        new ApiError('network', '无法连接后端服务，请确认服务已经启动。', {
          requestId: 'scope-list-1',
        }),
      )
      .mockResolvedValueOnce(createPage([], 1, 0))

    const wrapper = mount(DocumentScopePicker, {
      props: { modelValue: { kind: 'all' } },
    })
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('无法连接后端服务')
    expect(wrapper.text()).toContain('请求编号：scope-list-1')
    expect(wrapper.get('select').element.value).toBe('all')

    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(listDocumentsMock).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('当前没有解析完成的文档')
    wrapper.unmount()
  })
})
