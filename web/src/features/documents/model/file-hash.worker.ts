import { createSHA256 } from 'hash-wasm'
import type { FileHashWorkerCommand, FileHashWorkerEvent } from './file-hash-protocol'
import { FileHashCanceledError, hashFileIncrementally } from './incremental-file-hash'

interface HashWorkerScope {
  onmessage: ((event: MessageEvent<FileHashWorkerCommand>) => void) | null
  postMessage(message: FileHashWorkerEvent): void
}

const workerScope = self as unknown as HashWorkerScope
const canceledJobs = new Set<string>()
const hasherPromise = createSHA256()
let activeJobId: string | null = null

function post(message: FileHashWorkerEvent): void {
  workerScope.postMessage(message)
}

async function hashFile(jobId: string, file: File, requestedChunkSizeBytes: number): Promise<void> {
  if (activeJobId !== null) {
    post({ type: 'failed', jobId, message: '文件摘要计算器当前正忙。' })
    return
  }

  activeJobId = jobId
  const chunkSizeBytes = Math.max(64 * 1024, requestedChunkSizeBytes)

  try {
    const hasher = await hasherPromise
    const sha256 = await hashFileIncrementally(file, hasher, {
      chunkSizeBytes,
      isCanceled: () => canceledJobs.has(jobId),
      onProgress: (progress) => post({ type: 'progress', jobId, ...progress }),
    })
    post({ type: 'completed', jobId, sha256 })
  } catch (error) {
    if (error instanceof FileHashCanceledError) {
      post({ type: 'canceled', jobId })
      return
    }
    post({ type: 'failed', jobId, message: '无法计算文件摘要，请重试。' })
  } finally {
    canceledJobs.delete(jobId)
    activeJobId = null
  }
}

workerScope.onmessage = (event) => {
  const command = event.data
  if (command.type === 'cancel') {
    canceledJobs.add(command.jobId)
    return
  }

  void hashFile(command.jobId, command.file, command.chunkSizeBytes)
}
