/** 关键词检索当前支持的文档范围；多篇子集需要后端增加 document_ids 后再扩展。 */
export type DocumentScope =
  | { kind: 'all' }
  | {
      kind: 'single'
      documentId: number
    }

export interface ParsedDocumentScope {
  scope: DocumentScope
  isValid: boolean
}

/** 返回一个新的“全部文档”范围，避免组件共享可变对象引用。 */
export function allDocumentsScope(): DocumentScope {
  return { kind: 'all' }
}

/** 把 Router query 中的单数 document_id 转成稳定业务模型。 */
export function parseDocumentScopeQuery(value: unknown): ParsedDocumentScope {
  const firstValue = Array.isArray(value) ? value[0] : value

  if (firstValue === undefined || firstValue === null || firstValue === '') {
    return { scope: allDocumentsScope(), isValid: true }
  }

  if (typeof firstValue !== 'string' || !/^[1-9]\d*$/.test(firstValue)) {
    return { scope: allDocumentsScope(), isValid: false }
  }

  const documentId = Number(firstValue)
  if (!Number.isSafeInteger(documentId)) {
    return { scope: allDocumentsScope(), isValid: false }
  }

  return { scope: { kind: 'single', documentId }, isValid: true }
}

/** 只在单篇范围下生成后端现有的 document_id；全部范围返回 undefined。 */
export function documentIdFromScope(scope: DocumentScope): number | undefined {
  return scope.kind === 'single' ? scope.documentId : undefined
}
