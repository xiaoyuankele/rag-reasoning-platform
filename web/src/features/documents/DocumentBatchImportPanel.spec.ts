import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ResearchDocument } from '../../entities/document/model/document'
import { ApiError } from '../../shared/api/api-error'
import { getDocument, uploadDocument } from './api/document-api'
import { preflightDocument } from './api/document-preflight-api'
import {
  cancelProcessingJob,
  getLatestProcessingJobs,
  getProcessingJob,
  queueDocumentProcessing,
} from './api/processing-api'
import { createFileHashWorkerClient } from './model/file-hash-worker-client'
import DocumentBatchImportPanel from './ui/DocumentBatchImportPanel.vue'

vi.mock('./api/document-api', () => ({
  getDocument: vi.fn(),
  uploadDocument: vi.fn(),
}))

vi.mock('./api/document-preflight-api', () => ({
  preflightDocument: vi.fn(),
}))

vi.mock('./api/processing-api', () => ({
  cancelProcessingJob: vi.fn(),
  getLatestProcessingJobs: vi.fn(),
  getProcessingJob: vi.fn(),
  queueDocumentProcessing: vi.fn(),
}))

vi.mock('./model/file-hash-worker-client', () => ({
  createFileHashWorkerClient: vi.fn(),
}))

const getDocumentMock = vi.mocked(getDocument)
const uploadDocumentMock = vi.mocked(uploadDocument)
const preflightDocumentMock = vi.mocked(preflightDocument)
const getProcessingJobMock = vi.mocked(getProcessingJob)
const queueDocumentProcessingMock = vi.mocked(queueDocumentProcessing)
const getLatestProcessingJobsMock = vi.mocked(getLatestProcessingJobs)
const cancelProcessingJobMock = vi.mocked(cancelProcessingJob)
const createFileHashWorkerClientMock = vi.mocked(createFileHashWorkerClient)
const hashFileMock = vi.fn()
const disposeHashClientMock = vi.fn()
const fileSha256 = 'a'.repeat(64)

function readyDocument(id: number, name: string, sizeBytes = 3): ResearchDocument {
  return {
    id,
    title: null,
    originalName: name,
    mimeType: 'application/pdf',
    sizeBytes,
    sha256: fileSha256,
    status: 'ready',
    errorMessage: null,
    createdAt: new Date('2026-08-18T02:00:00Z'),
    updatedAt: new Date('2026-08-18T02:00:00Z'),
  }
}

async function selectFiles(wrapper: ReturnType<typeof mount>, files: File[]): Promise<void> {
  const input = wrapper.get<HTMLInputElement>('#document-files')
  Object.defineProperty(input.element, 'files', {
    configurable: true,
    value: files,
  })
  await input.trigger('change')
}

beforeEach(() => {
  getDocumentMock.mockReset()
  uploadDocumentMock.mockReset()
  preflightDocumentMock.mockReset()
  getProcessingJobMock.mockReset()
  queueDocumentProcessingMock.mockReset()
  getLatestProcessingJobsMock.mockReset()
  cancelProcessingJobMock.mockReset()
  createFileHashWorkerClientMock.mockReset()
  hashFileMock.mockReset()
  disposeHashClientMock.mockReset()

  hashFileMock.mockImplementation(async (file: File, options) => {
    options.onProgress?.({ processedBytes: file.size, totalBytes: file.size })
    return fileSha256
  })
  createFileHashWorkerClientMock.mockReturnValue({
    hash: hashFileMock,
    dispose: disposeHashClientMock,
  })
  preflightDocumentMock.mockResolvedValue({ exists: false, document: null })
  getLatestProcessingJobsMock.mockImplementation(async (documentIds) =>
    documentIds.map((documentId) => ({ documentId, job: null })),
  )
})

describe('DocumentBatchImportPanel', () => {
  it('一次选择多份文件并逐项展示导入结果', async () => {
    let nextId = 40
    uploadDocumentMock.mockImplementation(async (file) => {
      nextId += 1
      return { document: readyDocument(nextId, file.name, file.size), duplicate: true }
    })
    const wrapper = mount(DocumentBatchImportPanel)
    const files = [
      new File(['one'], 'one.pdf', { type: 'application/pdf' }),
      new File(['two'], 'two.pdf', { type: 'application/pdf' }),
    ]

    await selectFiles(wrapper, files)
    expect(wrapper.text()).toContain('共 2 份')
    expect(wrapper.text()).toContain('one.pdf')
    expect(wrapper.text()).toContain('two.pdf')

    await wrapper.get('.primary-button').trigger('click')
    await flushPromises()

    expect(uploadDocumentMock).toHaveBeenCalledTimes(2)
    expect(queueDocumentProcessingMock).not.toHaveBeenCalled()
    expect(wrapper.findAll('.import-status').map((status) => status.text())).toEqual([
      '已有内容 · 可用',
      '已有内容 · 可用',
    ])
    wrapper.unmount()
  })

  it('在加入队列前拒绝不支持的扩展名', async () => {
    const wrapper = mount(DocumentBatchImportPanel)

    await selectFiles(wrapper, [new File(['binary'], 'sample.bin')])

    expect(wrapper.text()).toContain('“sample.bin”：仅支持 PDF、Markdown 和纯文本文件。')
    expect(wrapper.text()).toContain('选择 PDF、Markdown 或纯文本文件')
    expect(uploadDocumentMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('展示单项失败的安全提示、请求编号和重试入口', async () => {
    uploadDocumentMock.mockRejectedValue(
      new ApiError('client', 'unsupported media type', {
        status: 415,
        requestId: 'batch-upload-1',
      }),
    )
    const wrapper = mount(DocumentBatchImportPanel)

    await selectFiles(wrapper, [new File(['pdf'], 'broken.pdf', { type: 'application/pdf' })])
    await wrapper.get('.primary-button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('文件内容不受支持')
    expect(wrapper.text()).toContain('请求编号：batch-upload-1')
    expect(wrapper.text()).toContain('重试失败项')
    wrapper.unmount()
  })

  it('预检发现已有内容时展示原文档并跳过上传', async () => {
    preflightDocumentMock.mockResolvedValue({
      exists: true,
      document: readyDocument(90, 'original.pdf'),
    })
    const wrapper = mount(DocumentBatchImportPanel)

    await selectFiles(wrapper, [new File(['one'], 'renamed.pdf', { type: 'application/pdf' })])
    await wrapper.get('.primary-button').trigger('click')
    await flushPromises()

    expect(preflightDocumentMock).toHaveBeenCalledTimes(1)
    expect(uploadDocumentMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('已有内容 · 可用')
    expect(wrapper.text()).toContain('已有文件：original.pdf')
    expect(wrapper.text()).toContain('文档 #90')
    expect(wrapper.text()).toContain('查看文档')
    wrapper.unmount()
  })

  it('上传容量暂满时展示 Retry-After，并保留手动重试入口', async () => {
    uploadDocumentMock.mockRejectedValue(
      new ApiError('server', 'busy', {
        status: 503,
        code: 'upload_capacity_exhausted',
        retryAfterSeconds: 2,
        requestId: 'upload-capacity-ui-1',
      }),
    )
    const wrapper = mount(DocumentBatchImportPanel)

    await selectFiles(wrapper, [new File(['pdf'], 'later.pdf', { type: 'application/pdf' })])
    await wrapper.get('.primary-button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('系统上传容量暂满')
    expect(wrapper.text()).toContain('2 秒后可手动重试失败项')
    expect(wrapper.text()).toContain('请求编号：upload-capacity-ui-1')
    expect(wrapper.get('.batch-actions .text-button').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
