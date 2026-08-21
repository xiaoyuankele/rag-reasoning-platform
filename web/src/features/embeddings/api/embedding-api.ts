import type {
  EmbeddingJob,
  EmbeddingJobStatus,
} from '../../../entities/embedding-job/model/embedding-job'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

const supportedStatuses = new Set<EmbeddingJobStatus>([
  'waiting_document',
  'queued',
  'processing',
  'succeeded',
  'failed',
  'canceled',
])

interface EmbeddingJobDto {
  id: number
  document_id: number
  model_name: string
  dimensions: number
  status: EmbeddingJobStatus
  attempt_count: number
  error_message: string | null
  next_attempt_at: string
  prompt_tokens: number | null
  total_tokens: number | null
  created_at: string
  updated_at: string
  started_at: string | null
  completed_at: string | null
}

interface EmbeddingBatchErrorDto {
  error: string
  code?: string
}

interface EmbeddingBatchItemDto {
  document_id: number
  outcome: EmbeddingBatchOutcome
  job?: EmbeddingJobDto
  error?: EmbeddingBatchErrorDto
}

interface EmbeddingBatchResponseDto {
  items: EmbeddingBatchItemDto[]
}

export type EmbeddingBatchOutcome = 'created' | 'already_active' | 'not_found' | 'failed'

export interface EmbeddingQueueResult {
  job: EmbeddingJob
  created: boolean
}

export interface EmbeddingBatchItem {
  documentId: number
  outcome: EmbeddingBatchOutcome
  job: EmbeddingJob | null
  errorMessage: string | null
  errorCode: string | null
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isInteger(value: unknown, minimum = 0): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= minimum
}

function isNullableInteger(value: unknown): value is number | null {
  return value === null || isInteger(value)
}

function isNullableString(value: unknown): value is string | null {
  return value === null || typeof value === 'string'
}

function isDateTime(value: unknown): value is string {
  return typeof value === 'string' && !Number.isNaN(Date.parse(value))
}

function isNullableDateTime(value: unknown): value is string | null {
  return value === null || isDateTime(value)
}

function isEmbeddingJobDto(value: unknown): value is EmbeddingJobDto {
  if (!isRecord(value)) return false

  return (
    isInteger(value.id, 1) &&
    isInteger(value.document_id, 1) &&
    typeof value.model_name === 'string' &&
    value.model_name.length > 0 &&
    isInteger(value.dimensions, 1) &&
    typeof value.status === 'string' &&
    supportedStatuses.has(value.status as EmbeddingJobStatus) &&
    isInteger(value.attempt_count) &&
    isNullableString(value.error_message) &&
    isDateTime(value.next_attempt_at) &&
    isNullableInteger(value.prompt_tokens) &&
    isNullableInteger(value.total_tokens) &&
    isDateTime(value.created_at) &&
    isDateTime(value.updated_at) &&
    isNullableDateTime(value.started_at) &&
    isNullableDateTime(value.completed_at)
  )
}

function mapEmbeddingJob(source: EmbeddingJobDto): EmbeddingJob {
  return {
    id: source.id,
    documentId: source.document_id,
    modelName: source.model_name,
    dimensions: source.dimensions,
    status: source.status,
    attemptCount: source.attempt_count,
    errorMessage: source.error_message,
    nextAttemptAt: new Date(source.next_attempt_at),
    promptTokens: source.prompt_tokens,
    totalTokens: source.total_tokens,
    createdAt: new Date(source.created_at),
    updatedAt: new Date(source.updated_at),
    startedAt: source.started_at === null ? null : new Date(source.started_at),
    completedAt: source.completed_at === null ? null : new Date(source.completed_at),
  }
}

/** 在运行时校验单个向量任务响应，避免未知状态直接进入界面。 */
export function mapEmbeddingJobResponse(data: unknown): EmbeddingJob {
  if (!isEmbeddingJobDto(data)) {
    throw new ApiError('invalid-response', '后端向量任务响应不符合约定。')
  }
  return mapEmbeddingJob(data)
}

function isBatchOutcome(value: unknown): value is EmbeddingBatchOutcome {
  return (
    value === 'created' || value === 'already_active' || value === 'not_found' || value === 'failed'
  )
}

function isBatchErrorDto(value: unknown): value is EmbeddingBatchErrorDto {
  if (!isRecord(value) || typeof value.error !== 'string' || value.error.length === 0) return false
  return value.code === undefined || typeof value.code === 'string'
}

/** 校验批量逐项结果；成功项必须有 job，失败项必须有安全错误。 */
export function mapEmbeddingBatchResponse(data: unknown): EmbeddingBatchItem[] {
  if (!isRecord(data) || !Array.isArray(data.items)) {
    throw new ApiError('invalid-response', '后端批量向量任务响应不符合约定。')
  }

  const source = data as unknown as EmbeddingBatchResponseDto
  const documentIds = new Set<number>()
  const mappedItems: EmbeddingBatchItem[] = []

  for (const item of source.items) {
    if (!isRecord(item) || !isInteger(item.document_id, 1) || !isBatchOutcome(item.outcome)) {
      throw new ApiError('invalid-response', '后端批量向量任务响应不符合约定。')
    }
    if (documentIds.has(item.document_id)) {
      throw new ApiError('invalid-response', '后端批量向量任务响应包含重复文档。')
    }
    documentIds.add(item.document_id)

    const succeeded = item.outcome === 'created' || item.outcome === 'already_active'
    const jobDto = isEmbeddingJobDto(item.job) ? item.job : null
    const errorDto = isBatchErrorDto(item.error) ? item.error : null
    if (succeeded && jobDto === null) {
      throw new ApiError('invalid-response', '后端批量向量任务响应不符合约定。')
    }
    if (!succeeded && errorDto === null) {
      throw new ApiError('invalid-response', '后端批量向量任务响应不符合约定。')
    }
    if (succeeded && jobDto?.document_id !== item.document_id) {
      throw new ApiError('invalid-response', '后端批量向量任务与文档不一致。')
    }

    mappedItems.push({
      documentId: item.document_id,
      outcome: item.outcome,
      job: succeeded && jobDto ? mapEmbeddingJob(jobDto) : null,
      errorMessage: succeeded ? null : (errorDto?.error ?? null),
      errorCode: succeeded ? null : (errorDto?.code ?? null),
    })
  }

  return mappedItems
}

/** 为一份文档创建或复用活动向量任务。 */
export async function queueEmbeddingJob(
  documentId: number,
  signal?: AbortSignal,
): Promise<EmbeddingQueueResult> {
  const response = await httpClient.post<unknown>(
    `/documents/${documentId}/embeddings`,
    undefined,
    {
      signal,
    },
  )
  if (response.status !== 200 && response.status !== 202) {
    throw new ApiError('invalid-response', '后端向量任务状态码不符合约定。')
  }
  const job = mapEmbeddingJobResponse(response.data)
  if (job.documentId !== documentId) {
    throw new ApiError('invalid-response', '后端向量任务与请求文档不一致。')
  }
  return { job, created: response.status === 202 }
}

/** 对最多 100 份文档逐项创建或复用向量任务。 */
export async function queueEmbeddingJobs(
  documentIds: number[],
  signal?: AbortSignal,
): Promise<EmbeddingBatchItem[]> {
  const response = await httpClient.post<unknown>(
    '/embedding-jobs/batch',
    { document_ids: documentIds },
    { signal },
  )
  const items = mapEmbeddingBatchResponse(response.data)
  const requestedIds = new Set(documentIds)
  if (
    items.length !== requestedIds.size ||
    items.some((item) => !requestedIds.has(item.documentId))
  ) {
    throw new ApiError('invalid-response', '后端批量向量任务未逐项返回请求结果。')
  }
  return items
}

/** 按任务 ID 获取当前用户可见的向量任务。 */
export async function getEmbeddingJob(jobId: number, signal?: AbortSignal): Promise<EmbeddingJob> {
  const response = await httpClient.get<unknown>(`/embedding-jobs/${jobId}`, { signal })
  const job = mapEmbeddingJobResponse(response.data)
  if (job.id !== jobId) {
    throw new ApiError('invalid-response', '后端向量任务与请求 ID 不一致。')
  }
  return job
}

/** 取消 waiting_document 或 queued 任务；后端负责并发状态校验。 */
export async function cancelEmbeddingJob(
  jobId: number,
  signal?: AbortSignal,
): Promise<EmbeddingJob> {
  const response = await httpClient.post<unknown>(`/embedding-jobs/${jobId}/cancel`, undefined, {
    signal,
  })
  const job = mapEmbeddingJobResponse(response.data)
  if (job.id !== jobId) {
    throw new ApiError('invalid-response', '后端取消任务响应与请求 ID 不一致。')
  }
  return job
}
