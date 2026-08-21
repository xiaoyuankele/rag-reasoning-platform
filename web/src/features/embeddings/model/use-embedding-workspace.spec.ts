import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { EmbeddingJob } from '../../../entities/embedding-job/model/embedding-job'
import type { DocumentPage, ResearchDocument } from '../../../entities/document/model/document'
import {
  cancelEmbeddingJob,
  getEmbeddingJob,
  queueEmbeddingJob,
  queueEmbeddingJobs,
} from '../api/embedding-api'
import {
  storedJobsKey,
  useEmbeddingWorkspace,
  type EmbeddingDocumentPageLoader,
} from './use-embedding-workspace'

vi.mock('../api/embedding-api', () => ({
  cancelEmbeddingJob: vi.fn(),
  getEmbeddingJob: vi.fn(),
  queueEmbeddingJob: vi.fn(),
  queueEmbeddingJobs: vi.fn(),
}))

const listDocumentsMock = vi.fn<EmbeddingDocumentPageLoader>()
const cancelEmbeddingJobMock = vi.mocked(cancelEmbeddingJob)
const getEmbeddingJobMock = vi.mocked(getEmbeddingJob)
const queueEmbeddingJobMock = vi.mocked(queueEmbeddingJob)
const queueEmbeddingJobsMock = vi.mocked(queueEmbeddingJobs)

function createDocument(
  id: number,
  status: ResearchDocument['status'] = 'ready',
): ResearchDocument {
  return {
    id,
    title: `Document ${id}`,
    originalName: `document-${id}.pdf`,
    mimeType: 'application/pdf',
    sizeBytes: 2048,
    sha256: id.toString(16).padStart(64, 'a').slice(-64),
    status,
    errorMessage: null,
    createdAt: new Date('2026-08-21T01:00:00Z'),
    updatedAt: new Date('2026-08-21T01:00:00Z'),
  }
}

function createPage(documents: ResearchDocument[]): DocumentPage {
  return {
    documents,
    pagination: { page: 1, pageSize: 100, total: documents.length, totalPages: 1 },
  }
}

function createJob(documentId: number, status: EmbeddingJob['status'] = 'queued'): EmbeddingJob {
  return {
    id: documentId + 100,
    documentId,
    modelName: 'text-embedding-v4',
    dimensions: 1536,
    status,
    attemptCount: status === 'succeeded' ? 1 : 0,
    errorMessage: null,
    nextAttemptAt: new Date('2026-08-21T02:00:00Z'),
    promptTokens: status === 'succeeded' ? 20 : null,
    totalTokens: status === 'succeeded' ? 20 : null,
    createdAt: new Date('2026-08-21T02:00:00Z'),
    updatedAt: new Date('2026-08-21T02:00:00Z'),
    startedAt: null,
    completedAt: status === 'succeeded' ? new Date('2026-08-21T02:00:02Z') : null,
  }
}

function createMemoryStorage(initial: Record<string, string> = {}) {
  const values = new Map(Object.entries(initial))
  return {
    getItem: vi.fn((key: string) => values.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => values.set(key, value)),
    removeItem: vi.fn((key: string) => values.delete(key)),
  }
}

beforeEach(() => {
  listDocumentsMock.mockReset()
  cancelEmbeddingJobMock.mockReset()
  getEmbeddingJobMock.mockReset()
  queueEmbeddingJobMock.mockReset()
  queueEmbeddingJobsMock.mockReset()
})

afterEach(() => vi.useRealTimers())

describe('useEmbeddingWorkspace', () => {
  it('单篇申请后集中轮询到成功，并保存会话恢复映射', async () => {
    vi.useFakeTimers()
    const storage = createMemoryStorage()
    const queuedJob = createJob(42)
    listDocumentsMock.mockResolvedValue(createPage([createDocument(42)]))
    queueEmbeddingJobMock.mockResolvedValue({ job: queuedJob, created: true })
    getEmbeddingJobMock.mockResolvedValue(createJob(42, 'succeeded'))

    const scope = effectScope()
    const workspace = scope.run(() =>
      useEmbeddingWorkspace({
        loadDocumentPage: listDocumentsMock,
        pollIntervalMs: 20,
        storage,
      }),
    )
    if (!workspace) throw new Error('embedding workspace was not created')

    await workspace.initialize()
    workspace.toggleDocument(42, true)
    await workspace.queueSelected()

    expect(queueEmbeddingJobMock).toHaveBeenCalledWith(42, expect.any(AbortSignal))
    expect(workspace.selectedCount.value).toBe(0)
    expect(workspace.jobsByDocumentId.value.get(42)?.status).toBe('queued')
    expect(storage.setItem).toHaveBeenCalledWith(storedJobsKey, JSON.stringify({ 42: 142 }))

    await vi.advanceTimersByTimeAsync(20)
    expect(getEmbeddingJobMock).toHaveBeenCalledWith(142, expect.any(AbortSignal))
    expect(workspace.jobsByDocumentId.value.get(42)?.status).toBe('succeeded')
    expect(workspace.activeJobCount.value).toBe(0)
    scope.stop()
  })

  it('批量逐项保留失败选择，并跟踪成功任务', async () => {
    const storage = createMemoryStorage()
    listDocumentsMock.mockResolvedValue(createPage([createDocument(42), createDocument(43)]))
    queueEmbeddingJobsMock.mockResolvedValue([
      {
        documentId: 42,
        outcome: 'created',
        job: createJob(42),
        errorMessage: null,
        errorCode: null,
      },
      {
        documentId: 43,
        outcome: 'not_found',
        job: null,
        errorMessage: 'document not found',
        errorCode: 'document_not_found',
      },
    ])

    const scope = effectScope()
    const workspace = scope.run(() =>
      useEmbeddingWorkspace({ loadDocumentPage: listDocumentsMock, storage }),
    )
    if (!workspace) throw new Error('embedding workspace was not created')

    await workspace.initialize()
    workspace.selectDocuments([42, 43])
    await workspace.queueSelected()

    expect(queueEmbeddingJobsMock).toHaveBeenCalledWith([42, 43], expect.any(AbortSignal))
    expect([...workspace.selectedDocumentIds.value]).toEqual([43])
    expect(workspace.jobsByDocumentId.value.get(42)?.status).toBe('queued')
    expect(workspace.feedbackByDocumentId.value.get(43)?.kind).toBe('error')
    expect(workspace.workspaceMessage.value?.message).toContain('失败 1')
    scope.stop()
  })

  it('刷新时恢复已知任务并允许取消排队任务', async () => {
    const storage = createMemoryStorage({ [storedJobsKey]: JSON.stringify({ 42: 142 }) })
    const queuedJob = createJob(42)
    listDocumentsMock.mockResolvedValue(createPage([createDocument(42)]))
    getEmbeddingJobMock.mockResolvedValue(queuedJob)
    cancelEmbeddingJobMock.mockResolvedValue(createJob(42, 'canceled'))

    const scope = effectScope()
    const workspace = scope.run(() =>
      useEmbeddingWorkspace({ loadDocumentPage: listDocumentsMock, storage }),
    )
    if (!workspace) throw new Error('embedding workspace was not created')

    await workspace.initialize()
    expect(workspace.jobsByDocumentId.value.get(42)?.status).toBe('queued')

    await workspace.cancel(42)
    expect(cancelEmbeddingJobMock).toHaveBeenCalledWith(142, expect.any(AbortSignal))
    expect(workspace.jobsByDocumentId.value.get(42)?.status).toBe('canceled')
    expect(workspace.feedbackByDocumentId.value.get(42)?.message).toContain('已取消')
    scope.stop()
  })
})
