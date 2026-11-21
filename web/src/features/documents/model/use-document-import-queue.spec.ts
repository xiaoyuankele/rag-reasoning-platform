import { effectScope } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ResearchDocument } from '../../../entities/document/model/document'
import type { ProcessingJob } from '../../../entities/processing-job/model/processing-job'
import { ApiError } from '../../../shared/api/api-error'
import { getDocument, uploadDocument } from '../api/document-api'
import { preflightDocument } from '../api/document-preflight-api'
import { getProcessingJob, queueDocumentProcessing } from '../api/processing-api'
import { createFileHashWorkerClient } from './file-hash-worker-client'
import { useDocumentImportQueue } from './use-document-import-queue'

vi.mock('../api/document-api', () => ({
  getDocument: vi.fn(),
  uploadDocument: vi.fn(),
}))

vi.mock('../api/document-preflight-api', () => ({
  preflightDocument: vi.fn(),
}))

vi.mock('../api/processing-api', () => ({
  getProcessingJob: vi.fn(),
  queueDocumentProcessing: vi.fn(),
}))

vi.mock('./file-hash-worker-client', () => ({
  createFileHashWorkerClient: vi.fn(),
}))

const getDocumentMock = vi.mocked(getDocument)
const uploadDocumentMock = vi.mocked(uploadDocument)
const preflightDocumentMock = vi.mocked(preflightDocument)
const getProcessingJobMock = vi.mocked(getProcessingJob)
const queueDocumentProcessingMock = vi.mocked(queueDocumentProcessing)
const createFileHashWorkerClientMock = vi.mocked(createFileHashWorkerClient)
const hashFileMock = vi.fn()
const disposeHashClientMock = vi.fn()
const fileSha256 = 'a'.repeat(64)

function createDocument(
  id: number,
  status: ResearchDocument['status'],
  sha256 = fileSha256,
  sizeBytes = 9,
): ResearchDocument {
  return {
    id,
    title: null,
    originalName: `document-${id}.pdf`,
    mimeType: 'application/pdf',
    sizeBytes,
    sha256,
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
  preflightDocumentMock.mockReset()
  getProcessingJobMock.mockReset()
  queueDocumentProcessingMock.mockReset()
  createFileHashWorkerClientMock.mockReset()
  hashFileMock.mockReset()
  disposeHashClientMock.mockReset()

  hashFileMock.mockResolvedValue(fileSha256)
  createFileHashWorkerClientMock.mockReturnValue({
    hash: hashFileMock,
    dispose: disposeHashClientMock,
  })
  preflightDocumentMock.mockResolvedValue({ exists: false, document: null })
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
    await vi.waitFor(() => expect(uploadDocumentMock).toHaveBeenCalledTimes(2))
    expect(maximumActiveUploads).toBe(2)

    releaseUploads.shift()?.()
    await vi.waitFor(() => expect(uploadDocumentMock).toHaveBeenCalledTimes(3))
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

  it('预检未命中但上传端发现重复时直接复用 ready 文档', async () => {
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

  it('预检命中已有文档时跳过正文上传', async () => {
    const readyDocument = createDocument(80, 'ready')
    preflightDocumentMock.mockResolvedValue({ exists: true, document: readyDocument })

    const scope = effectScope()
    const queue = scope.run(() => useDocumentImportQueue())
    if (!queue) throw new Error('queue composable was not created')

    queue.addFiles([pdf('same-content-new-name.pdf')])
    await queue.start()

    expect(hashFileMock).toHaveBeenCalledTimes(1)
    expect(preflightDocumentMock).toHaveBeenCalledWith(
      { sha256: fileSha256, sizeBytes: 9 },
      expect.any(AbortSignal),
    )
    expect(uploadDocumentMock).not.toHaveBeenCalled()
    expect(queue.items.value[0]).toMatchObject({
      state: 'duplicate',
      duplicate: true,
      document: readyDocument,
    })
    scope.stop()
  })

  it('预检网络或服务端故障时继续上传并保留降级提示', async () => {
    preflightDocumentMock.mockRejectedValue(
      new ApiError('server', '后端服务暂时不可用', {
        status: 500,
        requestId: 'preflight-500-1',
      }),
    )
    uploadDocumentMock.mockResolvedValue({
      document: createDocument(81, 'ready'),
      duplicate: false,
    })

    const scope = effectScope()
    const queue = scope.run(() => useDocumentImportQueue())
    if (!queue) throw new Error('queue composable was not created')

    queue.addFiles([pdf('new.pdf')])
    await queue.start()

    expect(uploadDocumentMock).toHaveBeenCalledTimes(1)
    expect(queue.items.value[0]).toMatchObject({
      state: 'ready',
      warningMessage: '预检暂时不可用，已改由上传接口完成最终重复检查。',
      warningRequestId: 'preflight-500-1',
    })
    scope.stop()
  })

  it('预检确定性拒绝时不绕过预检上传', async () => {
    preflightDocumentMock.mockRejectedValue(
      new ApiError('client', 'invalid document preflight', {
        status: 400,
        code: 'invalid_document_preflight',
        requestId: 'preflight-400-1',
      }),
    )

    const scope = effectScope()
    const queue = scope.run(() => useDocumentImportQueue())
    if (!queue) throw new Error('queue composable was not created')

    queue.addFiles([pdf('invalid.pdf')])
    await queue.start()

    expect(uploadDocumentMock).not.toHaveBeenCalled()
    expect(queue.items.value[0]).toMatchObject({
      state: 'check-failed',
      errorMessage: '文件摘要或大小未被后端接受，已停止上传。',
      requestId: 'preflight-400-1',
    })
    scope.stop()
  })
})
