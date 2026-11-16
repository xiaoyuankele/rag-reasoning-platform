export type AgentResponseLanguage = 'auto' | 'zh' | 'en'
export type AgentResolvedResponseLanguage = Exclude<AgentResponseLanguage, 'auto'>
export type AgentRunStatus = 'queued' | 'processing' | 'succeeded' | 'failed' | 'canceled'
export type AgentStepKind = 'tool_call' | 'final'
export type AgentStepStatus = 'pending' | 'running' | 'succeeded' | 'failed'
export type AgentToolName = 'search_documents' | 'read_document_evidence'
export type AgentExecutionErrorCode =
  | 'agent_execution_failed'
  | 'agent_temporarily_unavailable'
  | 'agent_step_limit_exceeded'
  | 'agent_token_budget_exceeded'
  | 'agent_deadline_exceeded'
  | 'agent_invalid_tool_call'
  | 'agent_tool_failed'
  | 'agent_worker_interrupted'

export interface AgentTokenUsage {
  promptTokens: number
  completionTokens: number
  totalTokens: number
}

export interface AgentExecutionError {
  code: AgentExecutionErrorCode
  message: string
}

export interface AgentSource {
  citation: number
  evidenceRef: string
  documentId: number
  chunkId: number
  title: string
  pageStart: number | null
  pageEnd: number | null
  contentHash: string
}

export interface AgentRunResult {
  answer: string
  responseLanguage: AgentResolvedResponseLanguage
  sources: AgentSource[]
}

export interface AgentStep {
  id: number
  agentRunId: number
  sequenceNo: number
  kind: AgentStepKind
  status: AgentStepStatus
  toolName: AgentToolName | null
  error: AgentExecutionError | null
  tokenUsage: AgentTokenUsage
  createdAt: Date
  startedAt: Date | null
  completedAt: Date | null
}

export interface AgentRun {
  id: number
  conversationId: number
  userMessageId: number
  assistantMessageId: number
  clientRunId: string
  requestedDocumentId: number | null
  requestedResponseLanguage: AgentResponseLanguage
  status: AgentRunStatus
  stepCount: number
  maxSteps: number
  maxTotalTokens: number
  cancelable: boolean
  steps: AgentStep[]
  result: AgentRunResult | null
  error: AgentExecutionError | null
  tokenUsage: AgentTokenUsage
  createdAt: Date
  updatedAt: Date
  startedAt: Date | null
  completedAt: Date | null
}

export const agentRunStatusLabels: Record<AgentRunStatus, string> = {
  queued: '排队中',
  processing: '执行中',
  succeeded: '已完成',
  failed: '已失败',
  canceled: '已取消',
}

export const agentStepStatusLabels: Record<AgentStepStatus, string> = {
  pending: '等待中',
  running: '执行中',
  succeeded: '已完成',
  failed: '已失败',
}

export const agentToolLabels: Record<AgentToolName, string> = {
  search_documents: '检索文档',
  read_document_evidence: '读取证据',
}

export function isAgentRunActive(status: AgentRunStatus): boolean {
  return status === 'queued' || status === 'processing'
}

export function isAgentRunTerminal(status: AgentRunStatus): boolean {
  return status === 'succeeded' || status === 'failed' || status === 'canceled'
}

/** 取消能力只读取服务端 DTO，不在浏览器重新推导。 */
export function canCancelAgentRun(run: AgentRun): boolean {
  return run.cancelable
}
