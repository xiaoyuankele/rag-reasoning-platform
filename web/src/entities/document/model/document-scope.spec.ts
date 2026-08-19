import { describe, expect, it } from 'vitest'
import { allDocumentsScope, documentIdFromScope, parseDocumentScopeQuery } from './document-scope'

describe('document scope', () => {
  it('把缺省 query 映射为全部文档范围', () => {
    expect(parseDocumentScopeQuery(undefined)).toEqual({
      scope: { kind: 'all' },
      isValid: true,
    })
    expect(documentIdFromScope(allDocumentsScope())).toBeUndefined()
  })

  it('把正整数 document_id 映射为单篇范围', () => {
    const parsed = parseDocumentScopeQuery(['42', '51'])

    expect(parsed).toEqual({
      scope: { kind: 'single', documentId: 42 },
      isValid: true,
    })
    expect(documentIdFromScope(parsed.scope)).toBe(42)
  })

  it.each(['0', '-1', '1.5', 'abc', '9007199254740992'])('拒绝无效 query：%s', (value) => {
    expect(parseDocumentScopeQuery(value)).toEqual({
      scope: { kind: 'all' },
      isValid: false,
    })
  })
})
