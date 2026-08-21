import { describe, expect, it, vi } from 'vitest'
import type { FileHashWorkerCommand, FileHashWorkerEvent } from './file-hash-protocol'
import { FileHashWorkerClient } from './file-hash-worker-client'

class FakeWorker {
  onmessage: ((event: MessageEvent<FileHashWorkerEvent>) => void) | null = null
  onerror: ((event: ErrorEvent) => void) | null = null
  readonly commands: FileHashWorkerCommand[] = []
  terminate = vi.fn()

  postMessage(command: FileHashWorkerCommand): void {
    this.commands.push(command)
  }

  emit(event: FileHashWorkerEvent): void {
    this.onmessage?.({ data: event } as MessageEvent<FileHashWorkerEvent>)
  }
}

function pdf(name: string): File {
  return new File(['pdf bytes'], name, { type: 'application/pdf' })
}

describe('FileHashWorkerClient', () => {
  it('复用单个 Worker 顺序处理任务并转发进度', async () => {
    const worker = new FakeWorker()
    const client = new FileHashWorkerClient(() => worker as unknown as Worker)
    const progress = vi.fn()

    const first = client.hash(pdf('one.pdf'), { jobId: 'one', onProgress: progress })
    const second = client.hash(pdf('two.pdf'), { jobId: 'two' })
    expect(worker.commands.map((command) => command.type)).toEqual(['hash-file'])

    worker.emit({ type: 'progress', jobId: 'one', processedBytes: 4, totalBytes: 9 })
    worker.emit({ type: 'completed', jobId: 'one', sha256: 'a'.repeat(64) })
    await expect(first).resolves.toBe('a'.repeat(64))
    expect(progress).toHaveBeenCalledWith({ processedBytes: 4, totalBytes: 9 })
    expect(worker.commands.at(-1)).toMatchObject({ type: 'hash-file', jobId: 'two' })

    worker.emit({ type: 'completed', jobId: 'two', sha256: 'b'.repeat(64) })
    await expect(second).resolves.toBe('b'.repeat(64))
    client.dispose()
  })

  it('活动任务取消时通知 Worker，并以 AbortError 结束', async () => {
    const worker = new FakeWorker()
    const client = new FileHashWorkerClient(() => worker as unknown as Worker)
    const controller = new AbortController()
    const hashing = client.hash(pdf('cancel.pdf'), {
      jobId: 'cancel',
      signal: controller.signal,
    })

    controller.abort()
    expect(worker.commands.at(-1)).toEqual({ type: 'cancel', jobId: 'cancel' })
    worker.emit({ type: 'canceled', jobId: 'cancel' })
    await expect(hashing).rejects.toMatchObject({ name: 'AbortError' })
    client.dispose()
  })

  it('拒绝不是小写64位十六进制的Worker结果', async () => {
    const worker = new FakeWorker()
    const client = new FileHashWorkerClient(() => worker as unknown as Worker)
    const hashing = client.hash(pdf('invalid.pdf'), { jobId: 'invalid' })

    worker.emit({ type: 'completed', jobId: 'invalid', sha256: 'A'.repeat(64) })
    await expect(hashing).rejects.toThrow('文件摘要计算结果不符合约定。')
    client.dispose()
  })
})
