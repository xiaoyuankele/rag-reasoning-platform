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
    expect(wrapper.text()).toContain('当前会话尚未跟踪向量任务')

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
})
