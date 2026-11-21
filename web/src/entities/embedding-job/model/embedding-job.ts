/** 后端当前支持的文档向量任务生命周期。 */
export type EmbeddingJobStatus =
  'waiting_document' | 'queued' | 'processing' | 'succeeded' | 'failed' | 'canceled'

/** 前端业务层使用的向量任务模型；字段名与后端 snake_case DTO 隔离。 */
export interface EmbeddingJob {
  id: number
  documentId: number
  modelName: string
  dimensions: number
  status: EmbeddingJobStatus
  attemptCount: number
  errorMessage: string | null
  nextAttemptAt: Date
  promptTokens: number | null
  totalTokens: number | null
  createdAt: Date
  updatedAt: Date
  startedAt: Date | null
  completedAt: Date | null
}

export const embeddingJobStatusLabels: Record<EmbeddingJobStatus, string> = {
  waiting_document: '等待文档解析',
  queued: '等待向量化',
  processing: '向量化中',
  succeeded: '最近任务成功',
  failed: '向量化失败',
  canceled: '已取消',
}

/** 只有活动任务需要继续向后端轮询。 */
export function isEmbeddingJobActive(status: EmbeddingJobStatus): boolean {
  return status === 'waiting_document' || status === 'queued' || status === 'processing'
}

/** 后端只允许取消尚未被 Worker 领取的任务。 */
export function canCancelEmbeddingJob(status: EmbeddingJobStatus): boolean {
  return status === 'waiting_document' || status === 'queued'
}
