import type { ApiError } from './api-error'

export type CapacityErrorCode =
  | 'embedding_owner_active_job_limit'
  | 'embedding_queue_capacity_exhausted'
  | 'embedding_provider_capacity_exhausted'
  | 'answer_capacity_exhausted'
  | 'answer_job_owner_queue_capacity'
  | 'answer_job_queue_capacity'
  | 'agent_run_owner_queue_capacity'
  | 'agent_run_queue_capacity'

export interface CapacityFailure {
  code: CapacityErrorCode
  title: string
  message: string
  retryAfterSeconds: number
  requestId?: string
}

interface CapacityPresentation {
  title: string
  message: string
}

const presentations: Record<CapacityErrorCode, CapacityPresentation> = {
  embedding_owner_active_job_limit: {
    title: '当前账户的活动向量任务已满',
    message: '请等待已有任务推进后，再提交尚未进入队列的文档。',
  },
  embedding_queue_capacity_exhausted: {
    title: '系统向量任务容量暂满',
    message: '系统正在处理其他向量任务，请稍后重新提交未成功的文档。',
  },
  embedding_provider_capacity_exhausted: {
    title: '在线向量服务暂时繁忙',
    message: '当前在线向量计算槽位已满，请稍后重试本次操作。',
  },
  answer_capacity_exhausted: {
    title: '问答服务暂时繁忙',
    message: '当前问答生成槽位已满，你的问题和检索范围已经保留。',
  },
  answer_job_owner_queue_capacity: {
    title: '当前账户的问答队列已满',
    message: '请等待已有回答任务推进后，再提交这个问题。',
  },
  answer_job_queue_capacity: {
    title: '系统问答队列暂时已满',
    message: '系统正在处理其他回答任务，请稍后重新提交这个问题。',
  },
  agent_run_owner_queue_capacity: {
    title: '当前账户的 Agent 队列已满',
    message: '请等待已有 Agent Run 推进后，再提交这个问题。',
  },
  agent_run_queue_capacity: {
    title: '系统 Agent 队列暂时已满',
    message: '系统正在处理其他 Agent Run，请稍后重新提交这个问题。',
  },
}

function isCapacityErrorCode(code: string | null | undefined): code is CapacityErrorCode {
  return (
    code !== null && code !== undefined && Object.prototype.hasOwnProperty.call(presentations, code)
  )
}

/**
 * 将正式容量 code 转为前端展示事实。
 * fallback 只在后端意外漏发 Retry-After 时防止立即重放，不代替服务端容量规则。
 */
export function createCapacityFailure(
  code: string | null | undefined,
  retryAfterSeconds: number | undefined,
  requestId: string | undefined,
  fallbackRetryAfterSeconds: number,
): CapacityFailure | null {
  if (!isCapacityErrorCode(code)) return null

  const presentation = presentations[code]
  const safeFallback = Math.max(1, Math.ceil(fallbackRetryAfterSeconds))
  const safeRetryAfter =
    retryAfterSeconds === undefined ? safeFallback : Math.max(0, Math.ceil(retryAfterSeconds))

  return {
    code,
    ...presentation,
    retryAfterSeconds: safeRetryAfter,
    requestId,
  }
}

/** 识别非 2xx 请求抛出的正式容量错误。 */
export function capacityFailureFromApiError(
  error: ApiError,
  fallbackRetryAfterSeconds: number,
): CapacityFailure | null {
  return createCapacityFailure(
    error.code,
    error.retryAfterSeconds,
    error.requestId,
    fallbackRetryAfterSeconds,
  )
}
