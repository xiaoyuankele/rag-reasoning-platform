import type { FileHashWorkerCommand, FileHashWorkerEvent } from './file-hash-protocol'

export interface FileHashProgress {
  processedBytes: number
  totalBytes: number
}

export interface HashFileOptions {
  jobId: string
  signal?: AbortSignal
  onProgress?: (progress: FileHashProgress) => void
}

export interface FileHashClient {
  hash(file: File, options: HashFileOptions): Promise<string>
  dispose(): void
}

interface HashTask {
  jobId: string
  file: File
  signal?: AbortSignal
  onProgress?: (progress: FileHashProgress) => void
  resolve: (sha256: string) => void
  reject: (error: unknown) => void
  abortListener: () => void
}

type WorkerFactory = () => Worker

const defaultChunkSizeBytes = 4 * 1024 * 1024
const lowercaseSha256Pattern = /^[0-9a-f]{64}$/

function createAbortError(): DOMException {
  return new DOMException('文件摘要计算已取消。', 'AbortError')
}

function defaultWorkerFactory(): Worker {
  return new Worker(new URL('./file-hash.worker.ts', import.meta.url), { type: 'module' })
}

/**
 * 复用一个 Dedicated Worker 顺序计算文件摘要。
 * 队列只允许一个活动哈希，避免多份大文件同时占用 CPU 和分块内存。
 */
export class FileHashWorkerClient implements FileHashClient {
  private readonly workerFactory: WorkerFactory
  private worker: Worker | null = null
  private activeTask: HashTask | null = null
  private readonly queuedTasks: HashTask[] = []
  private disposed = false

  constructor(workerFactory: WorkerFactory = defaultWorkerFactory) {
    this.workerFactory = workerFactory
  }

  hash(file: File, options: HashFileOptions): Promise<string> {
    if (this.disposed) return Promise.reject(createAbortError())
    if (options.signal?.aborted) return Promise.reject(createAbortError())

    return new Promise<string>((resolve, reject) => {
      const task = {} as HashTask
      task.jobId = options.jobId
      task.file = file
      task.signal = options.signal
      task.onProgress = options.onProgress
      task.resolve = resolve
      task.reject = reject
      task.abortListener = () => this.cancelTask(task)

      options.signal?.addEventListener('abort', task.abortListener, { once: true })
      this.queuedTasks.push(task)
      this.dispatchNext()
    })
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    this.worker?.terminate()
    this.worker = null

    if (this.activeTask) {
      this.cleanupTask(this.activeTask)
      this.activeTask.reject(createAbortError())
      this.activeTask = null
    }
    for (const task of this.queuedTasks.splice(0)) {
      this.cleanupTask(task)
      task.reject(createAbortError())
    }
  }

  private ensureWorker(): Worker {
    if (this.worker) return this.worker

    const worker = this.workerFactory()
    worker.onmessage = (event: MessageEvent<FileHashWorkerEvent>) => this.handleMessage(event.data)
    worker.onerror = () => this.handleWorkerFailure()
    this.worker = worker
    return worker
  }

  private dispatchNext(): void {
    if (this.disposed || this.activeTask || this.queuedTasks.length === 0) return

    const task = this.queuedTasks.shift()!
    if (task.signal?.aborted) {
      this.cleanupTask(task)
      task.reject(createAbortError())
      this.dispatchNext()
      return
    }

    this.activeTask = task
    const command: FileHashWorkerCommand = {
      type: 'hash-file',
      jobId: task.jobId,
      file: task.file,
      chunkSizeBytes: defaultChunkSizeBytes,
    }
    this.ensureWorker().postMessage(command)
  }

  private cancelTask(task: HashTask): void {
    const queuedIndex = this.queuedTasks.indexOf(task)
    if (queuedIndex >= 0) {
      this.queuedTasks.splice(queuedIndex, 1)
      this.cleanupTask(task)
      task.reject(createAbortError())
      return
    }

    if (this.activeTask === task && this.worker) {
      const command: FileHashWorkerCommand = { type: 'cancel', jobId: task.jobId }
      this.worker.postMessage(command)
    }
  }

  private handleMessage(message: FileHashWorkerEvent): void {
    const task = this.activeTask
    if (!task || message.jobId !== task.jobId) return

    if (message.type === 'progress') {
      task.onProgress?.({
        processedBytes: message.processedBytes,
        totalBytes: message.totalBytes,
      })
      return
    }

    if (message.type === 'completed') {
      if (!lowercaseSha256Pattern.test(message.sha256)) {
        this.finishActiveTask(new Error('文件摘要计算结果不符合约定。'))
      } else {
        this.finishActiveTask(undefined, message.sha256)
      }
      return
    }

    if (message.type === 'canceled') {
      this.finishActiveTask(createAbortError())
      return
    }

    this.finishActiveTask(new Error(message.message))
  }

  private handleWorkerFailure(): void {
    this.worker?.terminate()
    this.worker = null
    if (this.activeTask) {
      this.finishActiveTask(new Error('文件摘要计算器运行失败。'))
    }
  }

  private finishActiveTask(error?: unknown, sha256?: string): void {
    const task = this.activeTask
    if (!task) return

    this.activeTask = null
    this.cleanupTask(task)
    if (error !== undefined) task.reject(error)
    else task.resolve(sha256!)
    this.dispatchNext()
  }

  private cleanupTask(task: HashTask): void {
    task.signal?.removeEventListener('abort', task.abortListener)
  }
}

export function createFileHashWorkerClient(): FileHashClient {
  return new FileHashWorkerClient()
}
