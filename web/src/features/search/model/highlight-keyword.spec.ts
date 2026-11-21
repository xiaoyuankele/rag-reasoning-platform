import { describe, expect, it } from 'vitest'
import { highlightKeyword, highlightKeywords } from './highlight-keyword'

describe('highlightKeyword', () => {
  it('保留原文并高亮全部大小写不敏感命中', () => {
    expect(highlightKeyword('Bridge vibration and BRIDGE control', 'bridge')).toEqual([
      { text: 'Bridge', highlighted: true },
      { text: ' vibration and ', highlighted: false },
      { text: 'BRIDGE', highlighted: true },
      { text: ' control', highlighted: false },
    ])
  })

  it('把正则特殊字符当作普通关键词', () => {
    expect(highlightKeyword('Compare a+b with ab.', 'a+b')).toEqual([
      { text: 'Compare ', highlighted: false },
      { text: 'a+b', highlighted: true },
      { text: ' with ab.', highlighted: false },
    ])
  })

  it('没有命中时返回完整普通文本', () => {
    expect(highlightKeyword('maglev response', 'bridge')).toEqual([
      { text: 'maglev response', highlighted: false },
    ])
  })

  it('高亮多个关键词，并优先处理相互包含的较长关键词', () => {
    expect(
      highlightKeywords('railway vibration and rail response', ['rail', 'vibration', 'railway']),
    ).toEqual([
      { text: 'railway', highlighted: true },
      { text: ' ', highlighted: false },
      { text: 'vibration', highlighted: true },
      { text: ' and ', highlighted: false },
      { text: 'rail', highlighted: true },
      { text: ' response', highlighted: false },
    ])
  })
})
