import type {
  AnswerResponseLanguage,
  GroundedAnswerSource,
  GroundedAnswerUsage,
} from '../../answer/model/grounded-answer'

export type ConversationStatus = 'active' | 'archived'
export type ConversationMessageRole = 'user' | 'assistant'
export type ConversationMessageStatus = 'completed' | 'generating' | 'failed' | 'interrupted'
export type ConversationAnswerJobStatus =
  'queued' | 'processing' | 'succeeded' | 'failed' | 'canceled'

export interface Conversation {
  id: number
  title: string
  status: ConversationStatus
  lastMessageAt: Date | null
  createdAt: Date
  updatedAt: Date
}

export interface ConversationMessage {
  id: number
  conversationId: number
  sequenceNo: number
  role: ConversationMessageRole
  content: string
  status: ConversationMessageStatus
  clientMessageId: string | null
  createdAt: Date
  updatedAt: Date
}

export interface ConversationPagination {
  page: number
  pageSize: number
  total: number
  totalPages: number
}

export interface ConversationPage {
  conversations: Conversation[]
  pagination: ConversationPagination
}

export interface ConversationMessagePage {
  conversationId: number
  messages: ConversationMessage[]
  pagination: ConversationPagination
}

export interface ConversationCursor {
  next: string | null
  hasMore: boolean
}

export interface ConversationCursorPage {
  conversations: Conversation[]
  cursor: ConversationCursor
}

export interface ConversationMessageCursorPage {
  conversationId: number
  messages: ConversationMessage[]
  cursor: ConversationCursor
}

export interface ConversationAnswerJob {
  id: number
  documentId: number | null
  conversationId: number
  userMessageId: number
  assistantMessageId: number
  query: string
  topK: number
  requestedResponseLanguage: AnswerResponseLanguage
  status: ConversationAnswerJobStatus
  cancelable: boolean
  attemptCount: number
  errorCode: string | null
  errorMessage: string | null
  result: ConversationAnswerJobResult | null
  nextAttemptAt: Date
  createdAt: Date
  updatedAt: Date
  startedAt: Date | null
  completedAt: Date | null
}

export interface ConversationAnswerJobResult {
  answer: string
  responseLanguage: Exclude<AnswerResponseLanguage, 'auto'>
  sources: GroundedAnswerSource[]
  usage: GroundedAnswerUsage
}

export interface ConversationAnswerStartResult {
  conversationId: number
  userMessage: ConversationMessage
  assistantMessage: ConversationMessage
  answerJob: ConversationAnswerJob
}
