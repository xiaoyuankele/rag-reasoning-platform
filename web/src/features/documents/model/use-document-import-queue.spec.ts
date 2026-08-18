import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ResearchDocument } from '../../../entities/document/model/document'
import type { ProcessingJob } from '../../../entities/processing-job/model/processing-job'
import { ApiError } from '../../../shared/api/api-error'
import { getDocument, uploadDocument } from '../api/document-api'
import { getProcessingJob, queueDocumentProcessing } from '../api/processing-api'
import { useDocumentImportQueue } from './use-document-import-queue'

vi.mock('../api/document-api', () => ({
  getDocument: vi.fn(),
  uploadDocument: vi.fn(),
}))

vi.mock('../api/processing-api', () => ({
  getProcessingJob: vi.fn(),
  queueDocumentProcessing: vi.fn(),
}))

const getDocumentMock = vi.mocked(getDocument)
const uploadDocumentMock = vi.mocked(uploadDocument)
const getProcessingJobMock = vi.mocked(getProcessingJob)
const queueDocumentProcessingMock = vi.mocked(queueDocumentProcessing)

function createDocument(id: number, status: ResearchDocument['status']): ResearchDocument {
  return {
    id,
    title: null,
    originalName: `document-${id}.pdf`,
    mimeType: 'application/pdf',
    sizeBytes: 1024,
    sha256: id.toString(16).padStart(64, 'a').slice(-64),
    status,
    errorMessage: null,
    createdAt: new Date('2026-08-18T02:00:00Z'),
    updatedAt: new Date('2026-08-18T02:00:00Z'),
  }
}

function createJob(id: number, documentId: number, status: ProcessingJob['status']): ProcessingJob {
  return {
    id,
    documentId,
    status,
    attemptCount: status === 'queued' ? 0 : 1,
    errorMessage: null,
    createdAt: new Date('2026-08-18T02:00:01Z'),
    updatedAt: new Date('2026-08-18T02:00:01Z'),
    startedAt: status === 'queued' ? null : new Date('2026-08-18T02:00:02Z'),
    completedAt: status === 'succeeded' ? new Date('2026-08-18T02:00:03Z') : null,
  }
}

function pdf(name: string): File {
  return new File(['pdf bytes'], name, { type: 'application/pdf' })
}

beforeEach(() => {
  getDocumentMock.mockReset()
  uploadDocumentMock.mockReset()
  getProcessingJobMock.mockReset()
  queueDocumentProcessingMock.mockReset()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('useDocumentImportQueue', () => {
  it('上传并发不超过配置值', async () => {
    let activeUploads = 0
    let maximumActiveUploads = 0
    let nextDocumentId = 70
    const releaseUploads: Array<() => void> = []
    uploadDocumentMock.mockImplementation(
      (file) =>
        new Promise((resolve) => {
          activeUploads += 1
          maximumActiveUploads = Math.max(maximumActiveUploads, activeUploads)
          nextDocumentId += 1
          const documentId = nextDocumentId
          releaseUploads.push(() => {
            activeUploads -= 1
            resolve({ document: createDocument(documentId, 'ready'), duplicate: true })
          })
          expect(file.name).toMatch(/\.pdf$/)
        }),
    )

    const scope = effectScope()
    const queue = scope.run(() => useDocumentImportQueue({ uploadConcurrency: 2 }))
    if (!queue) throw new Error('queue composable was not created')

    queue.addFiles([pdf('one.pdf'), pdf('two.pdf'), pdf('three.pdf')])
    const startPromise = queue.start()
    await Promise.resolve()
    expect(uploadDocumentMock).toHaveBeenCalledTimes(2)
    expect(maximumActiveUploads).toBe(2)

    releaseUploads.shift()?.()
    await Promise.resolve()
    await Promise.resolve()
    expect(uploadDocumentMock).toHaveBeenCalledTimes(3)
    releaseUploads.splice(0).forEach((release) => release())
    await startPromise

    expect(maximumActiveUploads).toBe(2)
    expect(queue.summary.value.ready).toBe(3)
    scope.stop()
  })

  it('有限并发上传多份文件，并集中轮询到 ready', async () => {
    vi.useFakeTimers()
    let nextDocumentId = 10
    uploadDocumentMock.mockImplementation(async () => {
      nextDocumentId += 1
      return { document: createDocument(nextDocumentId, 'uploaded'), duplicate: false }
    })
    queueDocumentProcessingMock.mockImplementation(async (documentId) =>
      createJob(documentId + 100, documentId, 'queued'),
    )
    getProcessingJobMock.mockImplementation(async (jobId) =>
      createJob(jobId, jobId - 100, 'succeeded'),
    )
    getDocumentMock.mockImplementation(async (documentId) => createDocument(documentId, 'ready'))

    const scope = effectScope()
    const queue = scope.run(() =>
      useDocumentImportQueue({ uploadConcurrency: 2, pollIntervalMs: 20, pollBatchSize: 4 }),
    )
    if (!queue) throw new Error('queue composable was not created')

    queue.addFiles([pdf('one.pdf'), pdf('two.pdf')])
    await queue.start()

    expect(uploadDocumentMock).toHaveBeenCalledTimes(2)
    expect(queueDocumentProcessingMock).toHaveBeenCalledTimes(2)
    expect(queue.items.value.every((item) => item.state === 'queued')).toBe(true)

    await vi.advanceTimersByTimeAsync(20)

    expect(getProcessingJobMock).toHaveBeenCalledTimes(2)
    expect(queue.items.value.every((item) => item.state === 'ready')).toBe(true)
    expect(queue.summary.value.ready).toBe(2)
    scope.stop()
  })

  it('重复且 ready 的文档直接复用，不创建新的解析任务', async () => {
    const readyDocument = createDocument(42, 'ready')
    uploadDocumentMock.mockResolvedValue({ document: readyDocument, duplicate: true })

    const scope = effectScope()
    const queue = scope.run(() => useDocumentImportQueue())
    if (!queue) throw new Error('queue composable was not created')

    queue.addFiles([pdf('renamed-copy.pdf')])
    await queue.start()

    expect(queueDocumentProcessingMock).not.toHaveBeenCalled()
    expect(queue.items.value[0]).toMatchObject({
      state: 'ready',
      duplicate: true,
      document: readyDocument,
    })
    scope.stop()
  })

  it('活动任务 409 不算整批失败，并通过文档状态恢复到 ready', async () => {
    vi.useFakeTimers()
    const uploadedDocument = createDocument(42, 'uploaded')
    uploadDocumentMock.mockResolvedValue({ document: uploadedDocument, duplicate: true })
    queueDocumentProcessingMock.mockRejectedValue(
      new ApiError('conflict', 'document processing is already queued', { status: 409 }),
    )
    getDocumentMock
      .mockResolvedValueOnce({ ...uploadedDocument, status: 'processing' })
      .mockResolvedValueOnce(createDocument(42, 'ready'))

    const scope = effectScope()
    const queue = scope.run(() => useDocumentImportQueue({ pollIntervalMs: 20 }))
    if (!queue) throw new Error('queue composable was not created')

    queue.addFiles([pdf('duplicate.pdf')])
    await queue.start()
    expect(queue.items.value[0]?.state).toBe('queued')
    expect(queue.summary.value.failed).toBe(0)

    await vi.advanceTimersByTimeAsync(20)
    expect(queue.items.value[0]?.state).toBe('processing')
    await vi.advanceTimersByTimeAsync(20)
    expect(queue.items.value[0]?.state).toBe('ready')
    scope.stop()
  })

  it('单项上传失败不会阻止同批其他文件完成', async () => {
    uploadDocumentMock
      .mockRejectedValueOnce(
        new ApiError('client', 'file type must be supported', {
          status: 415,
          requestId: 'upload-bad-1',
        }),
      )
      .mockResolvedValueOnce({ document: createDocument(52, 'ready'), duplicate: true })

    const scope = effectScope()
    const queue = scope.run(() => useDocumentImportQueue({ uploadConcurrency: 1 }))
    if (!queue) throw new Error('queue composable was not created')

    queue.addFiles([pdf('bad.pdf'), pdf('good.pdf')])
    await queue.start()

    expect(queue.summary.value.failed).toBe(1)
    expect(queue.summary.value.ready).toBe(1)
    expect(queue.items.value[0]).toMatchObject({
      state: 'upload-failed',
      requestId: 'upload-bad-1',
    })
    scope.stop()
  })
})
