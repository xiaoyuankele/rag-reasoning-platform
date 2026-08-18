/** 后端文档解析任务的生命周期；与文档状态是两套不同状态机。 */
export type ProcessingJobStatus = 'queued' | 'processing' | 'succeeded' | 'failed'

export interface ProcessingJob {
  id: number
  documentId: number
  status: ProcessingJobStatus
  attemptCount: number
  errorMessage: string | null
  createdAt: Date
  updatedAt: Date
  startedAt: Date | null
  completedAt: Date | null
}
