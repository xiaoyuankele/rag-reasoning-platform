import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { EmbeddingJob } from '../../entities/embedding-job/model/embedding-job'
import type { DocumentPage, ResearchDocument } from '../../entities/document/model/document'
import { getEmbeddingJob, queueEmbeddingJob, queueEmbeddingJobs } from './api/embedding-api'
import type { EmbeddingDocumentPageLoader } from './model/use-embedding-workspace'
import EmbeddingWorkspacePanel from './ui/EmbeddingWorkspacePanel.vue'

vi.mock('./api/embedding-api', () => ({
  cancelEmbeddingJob: vi.fn(),
  getEmbeddingJob: vi.fn(),
  queueEmbeddingJob: vi.fn(),
  queueEmbeddingJobs: vi.fn(),
}))

const listDocumentsMock = vi.fn<EmbeddingDocumentPageLoader>()
const getEmbeddingJobMock = vi.mocked(getEmbeddingJob)
const queueEmbeddingJobMock = vi.mocked(queueEmbeddingJob)
const queueEmbeddingJobsMock = vi.mocked(queueEmbeddingJobs)

const readyDocument: ResearchDocument = {
  id: 42,
  title: 'Maglev study',
  originalName: 'maglev.pdf',
  mimeType: 'application/pdf',
  sizeBytes: 2048,
  sha256: 'a'.repeat(64),
  status: 'ready',
  errorMessage: null,
  createdAt: new Date('2026-08-21T01:00:00Z'),
  updatedAt: new Date('2026-08-21T01:00:00Z'),
}

const uploadedDocument: ResearchDocument = {
  ...readyDocument,
  id: 43,
  title: 'Waiting study',
  originalName: 'waiting.pdf',
  sha256: 'b'.repeat(64),
  status: 'uploaded',
}

const documentPage: DocumentPage = {
  documents: [readyDocument],
  pagination: { page: 1, pageSize: 100, total: 1, totalPages: 1 },
}

const queuedJob: EmbeddingJob = {
  id: 142,
  documentId: 42,
  modelName: 'text-embedding-v4',
  dimensions: 1536,
  status: 'queued',
  attemptCount: 0,
  errorMessage: null,
  nextAttemptAt: new Date('2026-08-21T02:00:00Z'),
  promptTokens: null,
  totalTokens: null,
  createdAt: new Date('2026-08-21T02:00:00Z'),
  updatedAt: new Date('2026-08-21T02:00:00Z'),
  startedAt: null,
  completedAt: null,
}

beforeEach(() => {
  sessionStorage.clear()
  listDocumentsMock.mockReset()
  getEmbeddingJobMock.mockReset()
  queueEmbeddingJobMock.mockReset()
  queueEmbeddingJobsMock.mockReset()
  listDocumentsMock.mockResolvedValue(documentPage)
})

describe('EmbeddingWorkspacePanel', () => {
  it('选择单篇文档、创建任务并展示后端状态', async () => {
    queueEmbeddingJobMock.mockResolvedValue({ job: queuedJob, created: true })
    const wrapper = mount(EmbeddingWorkspacePanel, {
      props: { loadDocumentPage: listDocumentsMock },
      global: {
        stubs: { RouterLink: { template: '<a><slot /></a>' } },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('Maglev study')
    expect(wrapper.text()).toContain('向量：当前会话未跟踪')

    await wrapper.get('input[type="checkbox"]').setValue(true)
    expect(wrapper.get('.primary-button').text()).toContain('开始向量化（1）')
    await wrapper.get('.primary-button').trigger('click')
    expect(wrapper.get('[role="alertdialog"]').text()).toContain('可能调用远程模型并消耗额度')
    expect(queueEmbeddingJobMock).not.toHaveBeenCalled()

    await wrapper.get('[role="alertdialog"] .primary-button').trigger('click')
    await flushPromises()

    expect(queueEmbeddingJobMock).toHaveBeenCalledWith(42, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('等待向量化')
    expect(wrapper.text()).toContain('任务 #142')
    wrapper.unmount()
  })

  it('支持按标题筛选且空结果不改变原始文档数据', async () => {
    const wrapper = mount(EmbeddingWorkspacePanel, {
      props: { loadDocumentPage: listDocumentsMock },
      global: {
        stubs: { RouterLink: { template: '<a><slot /></a>' } },
      },
    })
    await flushPromises()

    await wrapper.get('input[type="search"]').setValue('not-found')
    expect(wrapper.text()).toContain('没有符合筛选条件的文档')

    await wrapper.get('input[type="search"]').setValue('maglev')
    expect(wrapper.text()).toContain('Maglev study')
    expect(queueEmbeddingJobsMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('明确区分文本未解析与向量状态，并支持提交全部文档', async () => {
    listDocumentsMock.mockResolvedValue({
      documents: [readyDocument, uploadedDocument],
      pagination: { page: 1, pageSize: 100, total: 2, totalPages: 1 },
    })
    queueEmbeddingJobsMock.mockResolvedValue([
      {
        documentId: 42,
        outcome: 'created',
        job: queuedJob,
        errorMessage: null,
        errorCode: null,
      },
      {
        documentId: 43,
        outcome: 'created',
        job: { ...queuedJob, id: 143, documentId: 43, status: 'waiting_document' },
        errorMessage: null,
        errorCode: null,
      },
    ])

    const wrapper = mount(EmbeddingWorkspacePanel, {
      props: { loadDocumentPage: listDocumentsMock },
      global: {
        stubs: { RouterLink: { template: '<a><slot /></a>' } },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('文本已解析')
    expect(wrapper.text()).toContain('文本未解析')
    expect(wrapper.get('select').text()).toContain('未解析（1）')
    expect(wrapper.findAll('select')[1]?.text()).toContain('未跟踪（2）')

    const bulkButton = wrapper.get('.bulk-button')
    expect(bulkButton.text()).toContain('全部文档向量化（2）')
    await bulkButton.trigger('click')
    expect(wrapper.get('[role="alertdialog"]').text()).toContain('其中 1 份尚未完成文本解析')
    await wrapper.get('[role="alertdialog"] .primary-button').trigger('click')
    await flushPromises()

    expect(queueEmbeddingJobsMock).toHaveBeenCalledWith([42, 43], expect.any(AbortSignal))
    expect(wrapper.text()).toContain('全部文档处理完成')
    expect(wrapper.findAll('select')[1]?.text()).toContain('等待文本（1）')
    expect(wrapper.findAll('select')[1]?.text()).toContain('排队中（1）')

    await wrapper.findAll('select')[1]?.setValue('waiting_document')
    expect(wrapper.findAll('.document-row')).toHaveLength(1)
    expect(wrapper.text()).toContain('Waiting study')
    expect(wrapper.text()).not.toContain('Maglev study')
    wrapper.unmount()
  })
})
