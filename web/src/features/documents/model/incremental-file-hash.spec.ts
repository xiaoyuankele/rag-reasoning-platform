import { createSHA256 } from 'hash-wasm'
import { describe, expect, it, vi } from 'vitest'
import type { HashableFile } from './incremental-file-hash'
import { FileHashCanceledError, hashFileIncrementally } from './incremental-file-hash'

function hashableBytes(bytes: Uint8Array): HashableFile {
  return {
    size: bytes.byteLength,
    slice(start = 0, end = bytes.byteLength) {
      const chunk = bytes.slice(start, end)
      return {
        async arrayBuffer() {
          return chunk.buffer.slice(
            chunk.byteOffset,
            chunk.byteOffset + chunk.byteLength,
          ) as ArrayBuffer
        },
      }
    },
  }
}

describe('hashFileIncrementally', () => {
  it('跨多个分块计算标准 SHA-256 并报告单调进度', async () => {
    const progress = vi.fn()
    const sha256 = await hashFileIncrementally(
      hashableBytes(new TextEncoder().encode('abc')),
      await createSHA256(),
      { chunkSizeBytes: 1, onProgress: progress },
    )

    expect(sha256).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
    expect(progress.mock.calls.map(([value]) => value.processedBytes)).toEqual([1, 2, 3])
  })

  it('在分块边界之间响应协作式取消', async () => {
    let checks = 0
    await expect(
      hashFileIncrementally(hashableBytes(new Uint8Array([1, 2, 3, 4])), await createSHA256(), {
        chunkSizeBytes: 2,
        isCanceled: () => {
          checks += 1
          return checks >= 3
        },
      }),
    ).rejects.toBeInstanceOf(FileHashCanceledError)
  })
})
