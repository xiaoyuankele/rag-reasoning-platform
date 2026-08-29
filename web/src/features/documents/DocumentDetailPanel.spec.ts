import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DocumentChunkPage } from '../../entities/document/model/document-chunk'
import type { ResearchDocument } from '../../entities/document/model/document'
import type { ProcessingJob } from '../../entities/processing-job/model/processing-job'
import { listDocumentChunks } from './api/document-chunk-api'
import { deleteDocument, getDocument } from './api/document-api'
import {
  cancelProcessingJob,
  getLatestProcessingJobs,
  getProcessingJob,
  queueDocumentProcessing,
} from './api/processing-api'
import DocumentDetailPanel from './ui/DocumentDetailPanel.vue'

vi.mock('./api/document-api', () => ({
  deleteDocument: vi.fn(),
  getDocument: vi.fn(),
}))

vi.mock('./api/document-chunk-api', () => ({
  listDocumentChunks: vi.fn(),
}))

vi.mock('./api/processing-api', () => ({
  cancelProcessingJob: vi.fn(),
  getLatestProcessingJobs: vi.fn(),
  getProcessingJob: vi.fn(),
  queueDocumentProcessing: vi.fn(),
}))

const getDocumentMock = vi.mocked(getDocument)
const deleteDocumentMock = vi.mocked(deleteDocument)
const listDocumentChunksMock = vi.mocked(listDocumentChunks)
const queueDocumentProcessingMock = vi.mocked(queueDocumentProcessing)
const getProcessingJobMock = vi.mocked(getProcessingJob)
const getLatestProcessingJobsMock = vi.mocked(getLatestProcessingJobs)
const cancelProcessingJobMock = vi.mocked(cancelProcessingJob)

const uploadedDocument: ResearchDocument = {
  id: 42,
  title: 'Maglev control study',
  originalName: 'maglev.pdf',
  mimeType: 'application/pdf',
  sizeBytes: 2048,
  sha256: 'a'.repeat(64),
  status: 'uploaded',
  errorMessage: null,
  createdAt: new Date('2026-08-18T02:00:00Z'),
  updatedAt: new Date('2026-08-18T02:00:00Z'),
}

const queuedJob: ProcessingJob = {
  id: 7,
  documentId: 42,
  status: 'queued',
  cancelable: true,
  attemptCount: 0,
  errorMessage: null,
  createdAt: new Date('2026-08-18T02:00:01Z'),
  updatedAt: new Date('2026-08-18T02:00:01Z'),
  startedAt: null,
  completedAt: null,
}

const chunkPage: DocumentChunkPage = {
  documentId: 42,
  chunks: [
    {
      id: 5,
      index: 0,
      content: 'Parsed evidence from the document.',
      pageStart: 1,
      pageEnd: 1,
      createdAt: new Date('2026-08-18T02:00:03Z'),
    },
  ],
  pagination: { page: 1, pageSize: 10, total: 1, totalPages: 1 },
}

beforeEach(() => {
  getDocumentMock.mockReset()
  deleteDocumentMock.mockReset()
  listDocumentChunksMock.mockReset()
  queueDocumentProcessingMock.mockReset()
  getProcessingJobMock.mockReset()
  getLatestProcessingJobsMock.mockReset()
  cancelProcessingJobMock.mockReset()
  getDocumentMock.mockResolvedValue(uploadedDocument)
  deleteDocumentMock.mockResolvedValue()
  listDocumentChunksMock.mockResolvedValue(chunkPage)
  queueDocumentProcessingMock.mockResolvedValue(queuedJob)
  getProcessingJobMock.mockResolvedValue(queuedJob)
  getLatestProcessingJobsMock.mockResolvedValue([{ documentId: 42, job: null }])
  cancelProcessingJobMock.mockResolvedValue({
    ...queuedJob,
    status: 'canceled',
    cancelable: false,
    completedAt: new Date('2026-08-18T02:00:02Z'),
  })
})

describe('DocumentDetailPanel', () => {
  it('ready 文档自动读取并展示文本块，但不宣称已经向量化', async () => {
    getDocumentMock.mockResolvedValue({ ...uploadedDocument, status: 'ready' })
    const wrapper = mount(DocumentDetailPanel, { props: { documentId: 42 } })
    await flushPromises()

    expect(listDocumentChunksMock).toHaveBeenCalledWith(
      { documentId: 42, page: 1, pageSize: 10 },
      expect.any(AbortSignal),
    )
    expect(wrapper.text()).toContain('Parsed evidence from the document.')
    expect(wrapper.text()).toContain('这不代表已经完成向量化')
    wrapper.unmount()
  })

  it('创建解析任务后展示任务 ID 与排队状态', async () => {
    const wrapper = mount(DocumentDetailPanel, { props: { documentId: 42 } })
    await flushPromises()

    await wrapper.get('.processing-section .primary-button').trigger('click')
    await flushPromises()

    expect(queueDocumentProcessingMock).toHaveBeenCalledWith(42, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('任务已排队')
    expect(wrapper.text()).toContain('任务 #7')
    wrapper.unmount()
  })

  it('刷新后恢复 queued 任务，并把取消结果显示为后端终态', async () => {
    getLatestProcessingJobsMock.mockResolvedValue([{ documentId: 42, job: queuedJob }])
    const wrapper = mount(DocumentDetailPanel, { props: { documentId: 42 } })
    await flushPromises()

    expect(wrapper.text()).toContain('任务已排队')
    expect(queueDocumentProcessingMock).not.toHaveBeenCalled()
    await wrapper.get('.processing-section .text-button--danger').trigger('click')
    await flushPromises()

    expect(cancelProcessingJobMock).toHaveBeenCalledWith(7, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('解析任务已取消')
    wrapper.unmount()
  })

  it('只有二次确认后才删除并向父页面上报文档 ID', async () => {
    const wrapper = mount(DocumentDetailPanel, { props: { documentId: 42 } })
    await flushPromises()

    await wrapper.get('.danger-button').trigger('click')
    expect(deleteDocumentMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('确认删除“Maglev control study”吗？')

    await wrapper.get('.danger-button--solid').trigger('click')
    await flushPromises()

    expect(deleteDocumentMock).toHaveBeenCalledWith(42, expect.any(AbortSignal))
    expect(wrapper.emitted('deleted')).toEqual([[42]])
    wrapper.unmount()
  })
})
