import { beforeEach, describe, expect, it } from 'vitest'
import type { SemanticSearchResult } from '../../../entities/search-result/model/search-result'
import {
  readCachedSemanticSearch,
  removeLegacySearchCaches,
  writeCachedSemanticSearch,
} from './semantic-search-cache'

const result: SemanticSearchResult = {
  query: '悬浮稳定性',
  hits: [
    {
      chunkId: 11,
      documentId: 7,
      chunkIndex: 2,
      title: null,
      originalName: 'maglev.pdf',
      mimeType: 'application/pdf',
      content: '相关文本',
      pageStart: 3,
      pageEnd: 3,
      similarity: 0.9,
    },
  ],
}

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
})

describe('semantic search cache', () => {
  it('只恢复同一用户和同一文档范围的会话结果', () => {
    writeCachedSemanticSearch(17, { query: '悬浮稳定性', documentId: 7, topK: 5 }, result)

    expect(readCachedSemanticSearch(17, 7)).toEqual({
      params: { query: '悬浮稳定性', documentId: 7, topK: 5 },
      result,
    })
    expect(readCachedSemanticSearch(18, 7)).toBeNull()
    expect(readCachedSemanticSearch(17, 8)).toBeNull()
  })

  it('拒绝并清理结构损坏的浏览器缓存', () => {
    writeCachedSemanticSearch(17, { query: '悬浮稳定性', documentId: 7, topK: 5 }, result)
    const key = sessionStorage.key(0)
    expect(key).not.toBeNull()
    sessionStorage.setItem(key!, JSON.stringify({ version: 1, result: { hits: 'bad' } }))

    expect(readCachedSemanticSearch(17, 7)).toBeNull()
    expect(sessionStorage.getItem(key!)).toBeNull()
  })

  it('清理调试版本遗留的无用户隔离永久缓存', () => {
    localStorage.setItem('rag-workspace:keyword-search:last-result', 'private keyword result')
    localStorage.setItem('rag-workspace:semantic-search:last-result', 'private semantic result')

    removeLegacySearchCaches()

    expect(localStorage.length).toBe(0)
  })
})
