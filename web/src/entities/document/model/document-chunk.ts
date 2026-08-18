import type { DocumentPagination } from './document'

/** 已完成解析的文档文本块；chunkIndex 保留后端的原文顺序。 */
export interface DocumentChunk {
  id: number
  index: number
  content: string
  pageStart: number | null
  pageEnd: number | null
  createdAt: Date
}

export interface DocumentChunkPage {
  documentId: number
  chunks: DocumentChunk[]
  pagination: DocumentPagination
}
