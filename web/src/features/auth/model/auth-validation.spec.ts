import { describe, expect, it } from 'vitest'
import { isValidEmail, isValidVerificationCode, validatePassword } from './auth-validation'

describe('auth validation', () => {
  it('接受基础邮箱并拒绝缺少域名的值', () => {
    expect(isValidEmail(' learner@example.com ')).toBe(true)
    expect(isValidEmail('learner@localhost')).toBe(false)
  })

  it('只接受六位数字验证码', () => {
    expect(isValidVerificationCode('483921')).toBe(true)
    expect(isValidVerificationCode('48392a')).toBe(false)
  })

  it('镜像后端当前密码规则', () => {
    expect(validatePassword('Password123')).toBeUndefined()
    expect(validatePassword('password123')).toContain('大写字母')
    expect(validatePassword('Password!23')).toContain('只能包含')
  })
})
