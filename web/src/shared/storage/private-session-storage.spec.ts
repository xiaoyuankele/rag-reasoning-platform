import { beforeEach, describe, expect, it } from 'vitest'
import {
  clearAllPrivateSessionStorage,
  clearPrivateSessionStorageExcept,
  privateSessionStorageKey,
} from './private-session-storage'

beforeEach(() => sessionStorage.clear())

describe('private session storage', () => {
  it('只生成正整数用户命名空间', () => {
    expect(privateSessionStorageKey(17, 'semantic-search')).toBe(
      'rag-workspace:user:17:semantic-search',
    )
    expect(() => privateSessionStorageKey(0, 'semantic-search')).toThrow()
  })

  it('匿名化时只清理项目的全部用户私有键', () => {
    sessionStorage.setItem(privateSessionStorageKey(17, 'search'), 'a')
    sessionStorage.setItem(privateSessionStorageKey(18, 'search'), 'b')
    sessionStorage.setItem('unrelated:key', 'keep')

    clearAllPrivateSessionStorage()

    expect(sessionStorage.getItem(privateSessionStorageKey(17, 'search'))).toBeNull()
    expect(sessionStorage.getItem(privateSessionStorageKey(18, 'search'))).toBeNull()
    expect(sessionStorage.getItem('unrelated:key')).toBe('keep')
  })

  it('认证恢复时保留当前用户并清理其他用户', () => {
    sessionStorage.setItem(privateSessionStorageKey(17, 'search'), 'a')
    sessionStorage.setItem(privateSessionStorageKey(18, 'search'), 'b')

    clearPrivateSessionStorageExcept(18)

    expect(sessionStorage.getItem(privateSessionStorageKey(17, 'search'))).toBeNull()
    expect(sessionStorage.getItem(privateSessionStorageKey(18, 'search'))).toBe('b')
  })
})
