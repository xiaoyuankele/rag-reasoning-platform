const privateSessionPrefix = 'rag-workspace:user:'

function isPositiveInteger(value: number): boolean {
  return Number.isSafeInteger(value) && value > 0
}

/** 为当前用户构造只在本标签页会话中使用的私有存储键。 */
export function privateSessionStorageKey(ownerUserId: number, name: string): string {
  if (!isPositiveInteger(ownerUserId)) throw new Error('ownerUserId must be a positive integer')
  return `${privateSessionPrefix}${ownerUserId}:${name}`
}

function privateSessionKeys(): string[] {
  try {
    return Array.from({ length: sessionStorage.length }, (_, index) =>
      sessionStorage.key(index),
    ).filter((key): key is string => key !== null && key.startsWith(privateSessionPrefix))
  } catch {
    return []
  }
}

/** 匿名化时删除当前标签页内所有用户私有缓存。 */
export function clearAllPrivateSessionStorage(): void {
  try {
    for (const key of privateSessionKeys()) sessionStorage.removeItem(key)
  } catch {
    // 浏览器存储不可用时不阻塞认证状态转换。
  }
}

/** 恢复某一用户身份时，清理当前标签页可能残留的其他用户缓存。 */
export function clearPrivateSessionStorageExcept(ownerUserId: number): void {
  if (!isPositiveInteger(ownerUserId)) {
    clearAllPrivateSessionStorage()
    return
  }

  const retainedPrefix = `${privateSessionPrefix}${ownerUserId}:`
  try {
    for (const key of privateSessionKeys()) {
      if (!key.startsWith(retainedPrefix)) sessionStorage.removeItem(key)
    }
  } catch {
    // 浏览器存储不可用时不阻塞认证状态转换。
  }
}
