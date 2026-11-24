/**
 * Register/auth form validation mirror (spec 06§1, FL-01): email format,
 * password ≥8 with letters+numbers ≤72 bytes, WhatsApp digit normalisation —
 * each check maps to the exact `auth:validation.*` key the forms translate.
 */
import { describe, expect, it } from 'vitest'
import type { Rule } from 'antd/es/form'
import { toRule } from '../../lib/validation'
import { checkCode, checkEmail, checkName, checkPassword, checkRequired, checkWa } from './validation'

describe('form validation email rules', () => {
  it('flags empty and malformed addresses, accepts real ones', () => {
    expect(checkEmail('')).toBe('validation.required')
    expect(checkEmail('   ')).toBe('validation.required')
    expect(checkEmail('budi')).toBe('validation.invalidEmail')
    expect(checkEmail('budi@contoh')).toBe('validation.invalidEmail')
    expect(checkEmail('budi@contoh.id')).toBeNull()
    expect(checkEmail(`${'a'.repeat(330)}@x.id`)).toBe('validation.invalidEmail') // >320
  })
})

describe('form validation password rules', () => {
  it('requires 8+ characters containing letters AND numbers', () => {
    expect(checkPassword('')).toBe('validation.required')
    expect(checkPassword('abc12')).toBe('validation.passwordRule')
    expect(checkPassword('abcdefgh')).toBe('validation.passwordRule') // no digit
    expect(checkPassword('12345678')).toBe('validation.passwordRule') // no letter
    expect(checkPassword('abc12345')).toBeNull()
  })

  it('caps the password at 72 bytes', () => {
    expect(checkPassword('a1'.repeat(36))).toBeNull() // 72 chars/bytes
    expect(checkPassword(`${'a1'.repeat(36)}a`)).toBe('validation.passwordTooLong') // 73
  })
})

describe('form validation whatsapp rules', () => {
  it('accepts 0/8/62-prefixed local numbers with 8–15 digits', () => {
    expect(checkWa('081234567890')).toBeNull()
    expect(checkWa('6281234567890')).toBeNull()
    expect(checkWa('81234567890')).toBeNull()
    expect(checkWa('0812-3456-7890')).toBeNull() // separators stripped
  })

  it('separates digit-only mistakes from non-digit input', () => {
    expect(checkWa('')).toBe('validation.required')
    expect(checkWa('abc')).toBe('validation.required') // no digits at all
    expect(checkWa('0812')).toBe('validation.waInvalid') // too short
    expect(checkWa('123456789')).toBe('validation.waInvalid') // wrong prefix
    expect(checkWa('0812abc')).toBe('validation.waDigits') // digits but garbled
  })
})

describe('form validation remaining checks', () => {
  it('checkRequired / checkCode / checkName basics', () => {
    expect(checkRequired('x')).toBeNull()
    expect(checkRequired('  ')).toBe('validation.required')
    expect(checkCode('')).toBe('validation.required')
    expect(checkCode('abcd')).toBeNull()
    expect(checkName('Aisyah')).toBeNull()
    expect(checkName('')).toBe('validation.required')
    expect(checkName('x'.repeat(161))).toBe('validation.nameTooLong')
  })

  it('toRule adapts a check into a rejecting/resolving antd validator with the translated key', async () => {
    const translate = (key: string) => `<${key}>`
    const rule = toRule(checkRequired, translate) as Rule & {
      validator: (rule: unknown, value: unknown) => Promise<unknown>
    }

    await expect(rule.validator({}, '')).rejects.toThrow('<validation.required>')
    await expect(rule.validator({}, 'filled')).resolves.toBeUndefined()
    // Non-string form values are treated as empty, not as a crash.
    await expect(rule.validator({}, undefined)).rejects.toThrow('<validation.required>')
  })
})
