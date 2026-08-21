export interface HighlightedTextSegment {
  text: string
  highlighted: boolean
}

function escapeRegularExpression(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/**
 * 把后端返回的纯文本切成普通片段和命中片段。
 * 组件继续使用 Vue 文本插值渲染，不把文档内容写入 innerHTML。
 */
export function highlightKeywords(
  content: string,
  rawKeywords: string[],
): HighlightedTextSegment[] {
  const seenKeywords = new Set<string>()
  const keywords = rawKeywords
    .map((keyword) => keyword.trim())
    .filter((keyword) => {
      if (!keyword) return false
      const key = keyword.toLocaleLowerCase()
      if (seenKeywords.has(key)) return false
      seenKeywords.add(key)
      return true
    })
    .sort((left, right) => [...right].length - [...left].length)
  if (keywords.length === 0) return [{ text: content, highlighted: false }]

  const matcher = new RegExp(keywords.map(escapeRegularExpression).join('|'), 'giu')
  const segments: HighlightedTextSegment[] = []
  let cursor = 0

  for (const match of content.matchAll(matcher)) {
    const start = match.index
    const matchedText = match[0]
    if (start === undefined || !matchedText) continue

    if (start > cursor) {
      segments.push({ text: content.slice(cursor, start), highlighted: false })
    }
    segments.push({ text: matchedText, highlighted: true })
    cursor = start + matchedText.length
  }

  if (cursor === 0) return [{ text: content, highlighted: false }]
  if (cursor < content.length) {
    segments.push({ text: content.slice(cursor), highlighted: false })
  }

  return segments
}

export function highlightKeyword(content: string, rawQuery: string): HighlightedTextSegment[] {
  return highlightKeywords(content, [rawQuery])
}
