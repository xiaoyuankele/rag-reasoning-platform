/** 后端文档解析任务的生命周期；与文档状态是两套不同状态机。 */
export type ProcessingJobStatus = 'queued' | 'processing' | 'succeeded' | 'failed' | 'canceled'

export interface ProcessingJob {
  id: number
  documentId: number
  status: ProcessingJobStatus
  cancelable: boolean
  attemptCount: number
  errorMessage: string | null
  createdAt: Date
  updatedAt: Date
  startedAt: Date | null
  completedAt: Date | null
}

/** 只有排队和执行中的任务需要继续轮询。 */
export function isProcessingJobActive(status: ProcessingJobStatus): boolean {
  return status === 'queued' || status === 'processing'
}
