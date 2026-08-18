/** 后端当前支持的文档生命周期状态。 */
export type DocumentStatus = 'uploaded' | 'processing' | 'ready' | 'failed'

/** 文档在前端业务层使用的稳定模型，不暴露后端 storage_path。 */
export interface ResearchDocument {
  id: number
  title: string | null
  originalName: string
  mimeType: string
  sizeBytes: number
  sha256: string
  status: DocumentStatus
  errorMessage: string | null
  createdAt: Date
  updatedAt: Date
}

export interface DocumentPagination {
  page: number
  pageSize: number
  total: number
  totalPages: number
}

export interface DocumentPage {
  documents: ResearchDocument[]
  pagination: DocumentPagination
}

/** 上传成功结果；duplicate=true 仍然是成功，并指向已有文档。 */
export interface DocumentUploadResult {
  document: ResearchDocument
  duplicate: boolean
}
