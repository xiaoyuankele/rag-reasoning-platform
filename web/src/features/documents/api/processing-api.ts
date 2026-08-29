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
  'canceled',
])

interface ProcessingJobDto {
  id: number
  document_id: number
  status: ProcessingJobStatus
  cancelable: boolean
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
    typeof value.cancelable === 'boolean' &&
    value.cancelable === (value.status === 'queued') &&
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
    cancelable: data.cancelable,
    attemptCount: data.attempt_count,
    errorMessage: data.error_message,
    createdAt: new Date(data.created_at),
    updatedAt: new Date(data.updated_at),
    startedAt: data.started_at === null ? null : new Date(data.started_at),
    completedAt: data.completed_at === null ? null : new Date(data.completed_at),
  }
}

interface LatestProcessingJobItemDto {
  document_id: number
  job: ProcessingJobDto | null
}

export interface LatestProcessingJobItem {
  documentId: number
  job: ProcessingJob | null
}

/** 校验按文档发现的最新解析任务；null 不泄露文档是否存在。 */
export function mapLatestProcessingJobsResponse(data: unknown): LatestProcessingJobItem[] {
  if (!isRecord(data) || !Array.isArray(data.items)) {
    throw new ApiError('invalid-response', '后端最新解析任务响应不符合约定。')
  }

  const sourceItems = data.items as LatestProcessingJobItemDto[]
  const seenDocumentIds = new Set<number>()
  return sourceItems.map((item) => {
    if (!isRecord(item) || !isInteger(item.document_id, 1)) {
      throw new ApiError('invalid-response', '后端最新解析任务响应不符合约定。')
    }
    if (seenDocumentIds.has(item.document_id)) {
      throw new ApiError('invalid-response', '后端最新解析任务响应包含重复文档。')
    }
    seenDocumentIds.add(item.document_id)

    if (item.job !== null && !isProcessingJobDto(item.job)) {
      throw new ApiError('invalid-response', '后端最新解析任务响应不符合约定。')
    }
    if (item.job !== null && item.job.document_id !== item.document_id) {
      throw new ApiError('invalid-response', '后端最新解析任务与文档不一致。')
    }

    return {
      documentId: item.document_id,
      job: item.job === null ? null : mapProcessingJobResponse(item.job),
    }
  })
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

/** 按最多 100 个文档 ID 恢复当前用户可见的最新解析任务。 */
export async function getLatestProcessingJobs(
  documentIds: number[],
  signal?: AbortSignal,
): Promise<LatestProcessingJobItem[]> {
  const requestedIds = [...new Set(documentIds)]
  const response = await httpClient.post<unknown>(
    '/processing-jobs/latest',
    { document_ids: requestedIds },
    { signal },
  )
  if (response.status !== 200) {
    throw new ApiError('invalid-response', '后端最新解析任务状态码不符合约定。')
  }
  const items = mapLatestProcessingJobsResponse(response.data)
  if (
    items.length !== requestedIds.length ||
    items.some((item, index) => item.documentId !== requestedIds[index])
  ) {
    throw new ApiError('invalid-response', '后端最新解析任务未按请求顺序逐项返回。')
  }
  return items
}

/** 取消仍在 queued 的解析任务；是否可取消最终以后端原子状态判断为准。 */
export async function cancelProcessingJob(
  jobId: number,
  signal?: AbortSignal,
): Promise<ProcessingJob> {
  const response = await httpClient.post<unknown>(`/processing-jobs/${jobId}/cancel`, undefined, {
    signal,
  })
  if (response.status !== 200) {
    throw new ApiError('invalid-response', '后端取消解析任务状态码不符合约定。')
  }
  const job = mapProcessingJobResponse(response.data)
  if (job.id !== jobId) {
    throw new ApiError('invalid-response', '后端取消解析任务响应与请求 ID 不一致。')
  }
  return job
}
