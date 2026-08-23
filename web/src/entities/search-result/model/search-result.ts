/** 一个关键词命中的前端展示模型，已经与后端 snake_case DTO 隔离。 */
export interface KeywordSearchHit {
  chunkId: number
  documentId: number
  chunkIndex: number
  title: string | null
  originalName: string
  mimeType: string
  content: string
  pageStart: number | null
  pageEnd: number | null
}

/** 关键词检索分页信息。 */
export interface KeywordSearchPagination {
  page: number
  pageSize: number
  total: number
  totalPages: number
}

export type KeywordSearchOperator = 'all' | 'any'
export type KeywordSearchWithin = 'chunk'

/** 一次关键词检索返回的规范化结果页。 */
export interface KeywordSearchPage {
  query: string
  terms: string[]
  operator: KeywordSearchOperator | null
  within: KeywordSearchWithin | null
  results: KeywordSearchHit[]
  pagination: KeywordSearchPagination
}

/** 单条语义检索命中；similarity 是后端返回的向量相似度，不等同于概率。 */
export interface SemanticSearchHit {
  chunkId: number
  documentId: number
  chunkIndex: number
  title: string | null
  originalName: string
  mimeType: string
  content: string
  pageStart: number | null
  pageEnd: number | null
  similarity: number
}

/** 一次显式语义检索的规范化结果。 */
export interface SemanticSearchResult {
  query: string
  hits: SemanticSearchHit[]
}
