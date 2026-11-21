import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { EmbeddingJob } from '../../../entities/embedding-job/model/embedding-job'
import type { DocumentPage, ResearchDocument } from '../../../entities/document/model/document'
import { ApiError } from '../../../shared/api/api-error'
import {
  cancelEmbeddingJob,
  getEmbeddingJob,
  getLatestEmbeddingJobs,
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
  getLatestEmbeddingJobs: vi.fn(),
  queueEmbeddingJob: vi.fn(),
  queueEmbeddingJobs: vi.fn(),
}))

const listDocumentsMock = vi.fn<EmbeddingDocumentPageLoader>()
const cancelEmbeddingJobMock = vi.mocked(cancelEmbeddingJob)
const getEmbeddingJobMock = vi.mocked(getEmbeddingJob)
const getLatestEmbeddingJobsMock = vi.mocked(getLatestEmbeddingJobs)
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
  getLatestEmbeddingJobsMock.mockReset()
  queueEmbeddingJobMock.mockReset()
  queueEmbeddingJobsMock.mockReset()
  getLatestEmbeddingJobsMock.mockImplementation(async (documentIds) =>
    documentIds.map((documentId) => ({ documentId, job: null })),
  )
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
    queueEmbeddingJobsMock.mockResolvedValue({
      items: [
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
      ],
    })

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

  it('单篇达到用户容量后保留选择，并在 Retry-After 到期前阻止重复提交', async () => {
    vi.useFakeTimers()
    listDocumentsMock.mockResolvedValue(createPage([createDocument(42)]))
    queueEmbeddingJobMock
      .mockRejectedValueOnce(
        new ApiError('rate-limited', 'owner limit reached', {
          status: 429,
          code: 'embedding_owner_active_job_limit',
          requestId: 'embedding-owner-1',
          retryAfterSeconds: 5,
        }),
      )
      .mockResolvedValueOnce({ job: createJob(42), created: true })

    const scope = effectScope()
    const workspace = scope.run(() =>
      useEmbeddingWorkspace({
        loadDocumentPage: listDocumentsMock,
        storage: createMemoryStorage(),
      }),
    )!

    await workspace.initialize()
    workspace.toggleDocument(42, true)
    await workspace.queueSelected()

    expect(workspace.selectedCount.value).toBe(1)
    expect(workspace.capacityFailure.value?.code).toBe('embedding_owner_active_job_limit')
    expect(workspace.feedbackByDocumentId.value.get(42)?.kind).toBe('capacity')
    expect(workspace.retryAfterSeconds.value).toBe(5)
    expect(workspace.isCoolingDown.value).toBe(true)

    await workspace.queueSelected()
    expect(queueEmbeddingJobMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(5_000)
    await workspace.queueSelected()
    expect(queueEmbeddingJobMock).toHaveBeenCalledTimes(2)
    expect(workspace.selectedCount.value).toBe(0)
    scope.stop()
  })

  it('批量容量结果只暂缓失败项，并保留已成功任务和请求编号', async () => {
    vi.useFakeTimers()
    listDocumentsMock.mockResolvedValue(createPage([createDocument(42), createDocument(43)]))
    queueEmbeddingJobsMock.mockResolvedValue({
      requestId: 'embedding-queue-1',
      retryAfterSeconds: 5,
      items: [
        {
          documentId: 42,
          outcome: 'created',
          job: createJob(42),
          errorMessage: null,
          errorCode: null,
        },
        {
          documentId: 43,
          outcome: 'capacity_exhausted',
          job: null,
          errorMessage: 'queue capacity reached',
          errorCode: 'embedding_queue_capacity_exhausted',
        },
      ],
    })

    const scope = effectScope()
    const workspace = scope.run(() =>
      useEmbeddingWorkspace({
        loadDocumentPage: listDocumentsMock,
        storage: createMemoryStorage(),
      }),
    )!

    await workspace.initialize()
    workspace.selectDocuments([42, 43])
    await workspace.queueSelected()

    expect(workspace.jobsByDocumentId.value.get(42)?.status).toBe('queued')
    expect([...workspace.selectedDocumentIds.value]).toEqual([43])
    expect(workspace.feedbackByDocumentId.value.get(43)).toMatchObject({
      kind: 'capacity',
      requestId: 'embedding-queue-1',
    })
    expect(workspace.workspaceMessage.value).toMatchObject({
      kind: 'capacity',
      requestId: 'embedding-queue-1',
    })
    expect(workspace.workspaceMessage.value?.message).toContain('容量暂缓 1')
    scope.stop()
  })

  it('全部文档向量化按每批 100 份顺序拆分请求', async () => {
    const storage = createMemoryStorage()
    const documentIds = Array.from({ length: 201 }, (_, index) => index + 1)
    listDocumentsMock.mockResolvedValue(createPage(documentIds.map((id) => createDocument(id))))
    queueEmbeddingJobsMock.mockImplementation(async (batchIds) => ({
      items: batchIds.map((documentId) => ({
        documentId,
        outcome: 'created' as const,
        job: createJob(documentId),
        errorMessage: null,
        errorCode: null,
      })),
    }))

    const scope = effectScope()
    const workspace = scope.run(() =>
      useEmbeddingWorkspace({ loadDocumentPage: listDocumentsMock, storage }),
    )
    if (!workspace) throw new Error('embedding workspace was not created')

    await workspace.initialize()
    await workspace.queueAll(documentIds)

    expect(queueEmbeddingJobsMock).toHaveBeenCalledTimes(3)
    expect(queueEmbeddingJobsMock.mock.calls[0]?.[0]).toEqual(documentIds.slice(0, 100))
    expect(queueEmbeddingJobsMock.mock.calls[1]?.[0]).toEqual(documentIds.slice(100, 200))
    expect(queueEmbeddingJobsMock.mock.calls[2]?.[0]).toEqual(documentIds.slice(200))
    expect(workspace.workspaceMessage.value?.message).toContain(
      '全部文档处理完成：新建 201，复用 0，失败 0',
    )
    scope.stop()
  })

  it('初始化时从后端发现最新任务并允许取消排队任务', async () => {
    const storage = createMemoryStorage({ [storedJobsKey]: JSON.stringify({ 42: 142 }) })
    const queuedJob = createJob(42)
    listDocumentsMock.mockResolvedValue(createPage([createDocument(42)]))
    getLatestEmbeddingJobsMock.mockResolvedValue([{ documentId: 42, job: queuedJob }])
    cancelEmbeddingJobMock.mockResolvedValue(createJob(42, 'canceled'))

    const scope = effectScope()
    const workspace = scope.run(() =>
      useEmbeddingWorkspace({ loadDocumentPage: listDocumentsMock, storage }),
    )
    if (!workspace) throw new Error('embedding workspace was not created')

    await workspace.initialize()
    expect(getLatestEmbeddingJobsMock).toHaveBeenCalledWith([42], expect.any(AbortSignal))
    expect(getEmbeddingJobMock).not.toHaveBeenCalled()
    expect(workspace.jobsByDocumentId.value.get(42)?.status).toBe('queued')

    await workspace.cancel(42)
    expect(cancelEmbeddingJobMock).toHaveBeenCalledWith(142, expect.any(AbortSignal))
    expect(workspace.jobsByDocumentId.value.get(42)?.status).toBe('canceled')
    expect(workspace.feedbackByDocumentId.value.get(42)?.message).toContain('已取消')
    scope.stop()
  })

  it('发现阶段按 100 份拆批，并区分无任务与状态未知', async () => {
    const documents = Array.from({ length: 101 }, (_, index) => createDocument(index + 1))
    listDocumentsMock.mockResolvedValue(createPage(documents))
    getLatestEmbeddingJobsMock
      .mockResolvedValueOnce(
        documents.slice(0, 100).map((document) => ({
          documentId: document.id,
          job: document.id === 1 ? createJob(1, 'succeeded') : null,
        })),
      )
      .mockRejectedValueOnce(new Error('lookup unavailable'))

    const scope = effectScope()
    const workspace = scope.run(() =>
      useEmbeddingWorkspace({
        loadDocumentPage: listDocumentsMock,
        storage: createMemoryStorage(),
      }),
    )
    if (!workspace) throw new Error('embedding workspace was not created')

    await workspace.initialize()

    expect(getLatestEmbeddingJobsMock).toHaveBeenCalledTimes(2)
    expect(getLatestEmbeddingJobsMock.mock.calls[0]?.[0]).toHaveLength(100)
    expect(getLatestEmbeddingJobsMock.mock.calls[1]?.[0]).toEqual([101])
    expect(workspace.jobsByDocumentId.value.get(1)?.status).toBe('succeeded')
    expect(workspace.discoveredDocumentIds.value.has(100)).toBe(true)
    expect(workspace.discoveredDocumentIds.value.has(101)).toBe(false)
    expect(workspace.workspaceMessage.value?.message).toContain('向量任务状态恢复失败')
    scope.stop()
  })
})
