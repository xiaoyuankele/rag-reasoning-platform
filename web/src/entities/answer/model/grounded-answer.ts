export type AnswerResponseLanguage = 'auto' | 'zh' | 'en'

/** 一条与回答中 [n] 标记对应的后端证据来源。 */
export interface GroundedAnswerSource {
  citation: number
  chunkId: number
  documentId: number
  chunkIndex: number
  title: string | null
  originalName: string
  pageStart: number | null
  pageEnd: number | null
  similarity: number
}

/** 一次远程生成调用的 Token 用量；无证据降级时全部为 0。 */
export interface GroundedAnswerUsage {
  promptTokens: number
  completionTokens: number
  totalTokens: number
}

/** 带来源问答的前端模型，与后端 snake_case DTO 隔离。 */
export interface GroundedAnswer {
  query: string
  answer: string
  responseLanguage: AnswerResponseLanguage
  sources: GroundedAnswerSource[]
  usage: GroundedAnswerUsage
}
