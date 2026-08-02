import { describe, expect, it } from 'vitest'
import { currencySymbol, formatPaymentAmount, roundUpPaymentProduct } from '../currency'

describe('formatPaymentAmount', () => {
  it('uses the currency default fraction digits', () => {
    expect(formatPaymentAmount(100, 'JPY', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'KRW', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'HKD', 'en-US')).toContain('.00')
    expect(formatPaymentAmount(1.11, 'IQD', 'en-US')).toContain('1.110')
  })
})

describe('currencySymbol', () => {
  it('maps common payment currencies and falls back safely', () => {
    expect(currencySymbol('USD')).toBe('$')
    expect(currencySymbol('cny')).toBe('¥')
    expect(currencySymbol('EUR')).toBe('€')
    expect(currencySymbol('')).toBe('¥')
    expect(currencySymbol('XYZ')).toBe('XYZ')
  })
})

describe('roundUpPaymentProduct', () => {
  it.each([
    [0.49, 0.01, 0.01],
    [35.9, 1, 35.9],
    [35.9, 0.5, 17.95],
    [1, 0.333, 0.34],
    [0.1, 0.2, 0.02],
  ])('rounds %s multiplied by %s up to cents as %s', (amount, multiplier, expected) => {
    expect(roundUpPaymentProduct(amount, multiplier)).toBe(expected)
  })
})
