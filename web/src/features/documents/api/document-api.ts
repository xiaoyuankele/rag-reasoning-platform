import type {
  DocumentPage,
  DocumentPagination,
  DocumentStatus,
  DocumentUploadResult,
  ResearchDocument,
} from '../../../entities/document/model/document'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

const supportedStatuses = new Set<DocumentStatus>(['uploaded', 'processing', 'ready', 'failed'])

interface DocumentDto {
  id: number
  title: string | null
  original_name: string
  mime_type: string
  size_bytes: number
  sha256: string
  status: DocumentStatus
  error_message: string | null
  created_at: string
  updated_at: string
}

interface DocumentPaginationDto {
  page: number
  page_size: number
  total: number
  total_pages: number
}

interface DocumentListResponseDto {
  documents: DocumentDto[]
  pagination: DocumentPaginationDto
}

interface DocumentUploadResponseDto extends DocumentDto {
  duplicate: boolean
}

export interface ListDocumentsParams {
  page: number
  pageSize: number
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isInteger(value: unknown, minimum = 0): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum
}

function isNullableString(value: unknown): value is string | null {
  return typeof value === 'string' || value === null
}

function isDateTime(value: unknown): value is string {
  return typeof value === 'string' && !Number.isNaN(Date.parse(value))
}

function isDocumentDto(value: unknown): value is DocumentDto {
  if (!isRecord(value)) return false

  return (
    isInteger(value.id, 1) &&
    isNullableString(value.title) &&
    typeof value.original_name === 'string' &&
    value.original_name.length > 0 &&
    typeof value.mime_type === 'string' &&
    value.mime_type.length > 0 &&
    isInteger(value.size_bytes) &&
    typeof value.sha256 === 'string' &&
    /^[a-f\d]{64}$/i.test(value.sha256) &&
    typeof value.status === 'string' &&
    supportedStatuses.has(value.status as DocumentStatus) &&
    isNullableString(value.error_message) &&
    isDateTime(value.created_at) &&
    isDateTime(value.updated_at)
  )
}

function isPaginationDto(value: unknown): value is DocumentPaginationDto {
  if (!isRecord(value)) return false

  return (
    isInteger(value.page, 1) &&
    isInteger(value.page_size, 1) &&
    isInteger(value.total) &&
    isInteger(value.total_pages)
  )
}

function mapDocument(source: DocumentDto): ResearchDocument {
  return {
    id: source.id,
    title: source.title,
    originalName: source.original_name,
    mimeType: source.mime_type,
    sizeBytes: source.size_bytes,
    sha256: source.sha256,
    status: source.status,
    errorMessage: source.error_message,
    createdAt: new Date(source.created_at),
    updatedAt: new Date(source.updated_at),
  }
}

function mapPagination(source: DocumentPaginationDto): DocumentPagination {
  return {
    page: source.page,
    pageSize: source.page_size,
    total: source.total,
    totalPages: source.total_pages,
  }
}

/** 校验 GET /documents 的运行时响应并隔离 snake_case DTO。 */
export function mapDocumentListResponse(data: unknown): DocumentPage {
  if (!isRecord(data)) {
    throw new ApiError('invalid-response', '后端文档列表响应不符合约定。')
  }

  const source = data as unknown as DocumentListResponseDto
  if (
    !Array.isArray(source.documents) ||
    !source.documents.every(isDocumentDto) ||
    !isPaginationDto(source.pagination)
  ) {
    throw new ApiError('invalid-response', '后端文档列表响应不符合约定。')
  }

  return {
    documents: source.documents.map(mapDocument),
    pagination: mapPagination(source.pagination),
  }
}

/**
 * 校验上传 DTO 与 HTTP 状态组合。
 * 201 必须表示新建，200 必须表示命中已有内容，防止界面误判重复上传。
 */
export function mapDocumentUploadResponse(data: unknown, status: number): DocumentUploadResult {
  if (!isDocumentDto(data) || !isRecord(data) || typeof data.duplicate !== 'boolean') {
    throw new ApiError('invalid-response', '后端文档上传响应不符合约定。')
  }

  const source = data as unknown as DocumentUploadResponseDto
  const statusMatchesResult =
    (status === 201 && source.duplicate === false) || (status === 200 && source.duplicate === true)

  if (!statusMatchesResult) {
    throw new ApiError('invalid-response', '后端文档上传状态与响应不一致。')
  }

  return {
    document: mapDocument(source),
    duplicate: source.duplicate,
  }
}

/** 获取当前登录用户自己的文档分页。 */
export async function listDocuments(
  params: ListDocumentsParams,
  signal?: AbortSignal,
): Promise<DocumentPage> {
  const response = await httpClient.get<unknown>('/documents', {
    params: {
      page: params.page,
      page_size: params.pageSize,
    },
    signal,
  })

  return mapDocumentListResponse(response.data)
}

/** 上传单个文件；浏览器只发送原始文件，内容哈希和最终去重由后端完成。 */
export async function uploadDocument(
  file: File,
  signal?: AbortSignal,
): Promise<DocumentUploadResult> {
  const body = new FormData()
  body.append('file', file, file.name)

  const response = await httpClient.post<unknown>('/documents', body, {
    signal,
    timeout: 120_000,
  })

  return mapDocumentUploadResponse(response.data, response.status)
}
