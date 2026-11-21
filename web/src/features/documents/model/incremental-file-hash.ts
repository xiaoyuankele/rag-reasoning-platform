import type { IHasher } from 'hash-wasm'
import type { FileHashProgress } from './file-hash-worker-client'

interface HashableSlice {
  arrayBuffer(): Promise<ArrayBuffer>
}

export interface HashableFile {
  size: number
  slice(start?: number, end?: number): HashableSlice
}

interface IncrementalFileHashOptions {
  chunkSizeBytes: number
  isCanceled?: () => boolean
  onProgress?: (progress: FileHashProgress) => void
}

export class FileHashCanceledError extends Error {
  constructor() {
    super('文件摘要计算已取消。')
    this.name = 'FileHashCanceledError'
  }
}

/** 按固定大小读取文件并增量更新 SHA-256，内存不随完整文件大小线性增长。 */
export async function hashFileIncrementally(
  file: HashableFile,
  hasher: IHasher,
  options: IncrementalFileHashOptions,
): Promise<string> {
  if (!Number.isSafeInteger(options.chunkSizeBytes) || options.chunkSizeBytes <= 0) {
    throw new Error('文件摘要分块大小必须是正整数。')
  }

  hasher.init()
  let processedBytes = 0
  while (processedBytes < file.size) {
    if (options.isCanceled?.()) throw new FileHashCanceledError()

    const nextOffset = Math.min(file.size, processedBytes + options.chunkSizeBytes)
    const buffer = await file.slice(processedBytes, nextOffset).arrayBuffer()
    if (options.isCanceled?.()) throw new FileHashCanceledError()

    hasher.update(new Uint8Array(buffer))
    processedBytes = nextOffset
    options.onProgress?.({ processedBytes, totalBytes: file.size })
  }

  return hasher.digest('hex')
}
