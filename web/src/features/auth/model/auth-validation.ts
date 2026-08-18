const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/
const verificationCodePattern = /^\d{6}$/
const passwordPattern = /^[A-Za-z0-9]+$/

export function isValidEmail(value: string): boolean {
  return emailPattern.test(value.trim())
}

export function isValidVerificationCode(value: string): boolean {
  return verificationCodePattern.test(value)
}

/** 镜像后端当前密码规则，只用于即时提示，不能替代后端安全校验。 */
export function validatePassword(value: string): string | undefined {
  if (value.length < 8 || value.length > 128) return '密码长度需要在 8～128 个字符之间。'
  if (!passwordPattern.test(value)) return '密码只能包含英文字母和数字。'
  if (!/[A-Z]/.test(value) || !/[a-z]/.test(value) || !/\d/.test(value)) {
    return '密码必须同时包含大写字母、小写字母和数字。'
  }
  return undefined
}
