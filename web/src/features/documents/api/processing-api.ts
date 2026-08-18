import type {
  ProcessingJob,
  ProcessingJobStatus,
} from '../../../entities/processing-job/model/processing-job'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

const supportedStatuses = new Set<ProcessingJobStatus>([
  'queued',
  'processing',
  'succeeded',
  'failed',
])

interface ProcessingJobDto {
  id: number
  document_id: number
  status: ProcessingJobStatus
  attempt_count: number
  error_message: string | null
  created_at: string
  updated_at: string
  started_at: string | null
  completed_at: string | null
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

function isNullableDateTime(value: unknown): value is string | null {
  return value === null || isDateTime(value)
}

function isProcessingJobDto(value: unknown): value is ProcessingJobDto {
  if (!isRecord(value)) return false

  return (
    isInteger(value.id, 1) &&
    isInteger(value.document_id, 1) &&
    typeof value.status === 'string' &&
    supportedStatuses.has(value.status as ProcessingJobStatus) &&
    isInteger(value.attempt_count) &&
    isNullableString(value.error_message) &&
    isDateTime(value.created_at) &&
    isDateTime(value.updated_at) &&
    isNullableDateTime(value.started_at) &&
    isNullableDateTime(value.completed_at)
  )
}

/** 把公开任务 DTO 转换成前端模型，并拒绝未知状态或非法时间。 */
export function mapProcessingJobResponse(data: unknown): ProcessingJob {
  if (!isProcessingJobDto(data)) {
    throw new ApiError('invalid-response', '后端解析任务响应不符合约定。')
  }

  return {
    id: data.id,
    documentId: data.document_id,
    status: data.status,
    attemptCount: data.attempt_count,
    errorMessage: data.error_message,
    createdAt: new Date(data.created_at),
    updatedAt: new Date(data.updated_at),
    startedAt: data.started_at === null ? null : new Date(data.started_at),
    completedAt: data.completed_at === null ? null : new Date(data.completed_at),
  }
}

/** 创建解析任务；后端必须用 202 表示异步任务已经入队。 */
export async function queueDocumentProcessing(
  documentId: number,
  signal?: AbortSignal,
): Promise<ProcessingJob> {
  const response = await httpClient.post<unknown>(`/documents/${documentId}/process`, undefined, {
    signal,
  })

  if (response.status !== 202) {
    throw new ApiError('invalid-response', '后端创建解析任务响应不符合约定。')
  }

  const job = mapProcessingJobResponse(response.data)
  if (job.documentId !== documentId) {
    throw new ApiError('invalid-response', '后端解析任务与当前文档不一致。')
  }
  return job
}

/** 查询当前用户拥有的解析任务。 */
export async function getProcessingJob(
  jobId: number,
  signal?: AbortSignal,
): Promise<ProcessingJob> {
  const response = await httpClient.get<unknown>(`/processing-jobs/${jobId}`, { signal })
  const job = mapProcessingJobResponse(response.data)
  if (job.id !== jobId) {
    throw new ApiError('invalid-response', '后端返回了不匹配的解析任务。')
  }
  return job
}
