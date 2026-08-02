export const DEFAULT_PAYMENT_CURRENCY = 'CNY'

const ZERO_DECIMAL_PAYMENT_CURRENCIES = new Set([
  'BIF', 'CLP', 'DJF', 'GNF', 'ISK', 'JPY', 'KMF', 'KRW', 'MGA',
  'PYG', 'RWF', 'UGX', 'VND', 'VUV', 'XAF', 'XOF', 'XPF',
])

const THREE_DECIMAL_PAYMENT_CURRENCIES = new Set([
  'BHD', 'IQD', 'JOD', 'KWD', 'LYD', 'OMR', 'TND',
])

const PAYMENT_CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$',
  CNY: '¥',
  RMB: '¥',
  EUR: '€',
  GBP: '£',
  JPY: '¥',
  HKD: 'HK$',
  TWD: 'NT$',
  KRW: '₩',
  AUD: 'A$',
  CAD: 'C$',
  SGD: 'S$',
  NZD: 'NZ$',
  MOP: 'MOP$',
  MYR: 'RM',
  THB: '฿',
  PHP: '₱',
  INR: '₹',
}

export function normalizePaymentCurrency(currency?: string | null): string {
  const normalized = String(currency || '').trim().toUpperCase()
  return /^[A-Z]{3}$/.test(normalized) ? normalized : DEFAULT_PAYMENT_CURRENCY
}

export function currencySymbol(currency?: string | null): string {
  const normalized = normalizePaymentCurrency(currency)
  return PAYMENT_CURRENCY_SYMBOLS[normalized] || normalized
}

export function paymentCurrencyFractionDigits(currency?: string | null): number {
  const normalized = normalizePaymentCurrency(currency)
  if (ZERO_DECIMAL_PAYMENT_CURRENCIES.has(normalized)) return 0
  if (THREE_DECIMAL_PAYMENT_CURRENCIES.has(normalized)) return 3
  return 2
}

function decimalInteger(value: number): { coefficient: bigint; scale: number } {
  const [mantissa, rawExponent = '0'] = Math.abs(value).toString().toLowerCase().split('e')
  const [integer, fraction = ''] = mantissa.split('.')
  const digits = `${integer}${fraction}`.replace(/^0+(?=\d)/, '') || '0'
  return {
    coefficient: BigInt(digits),
    scale: fraction.length - Number(rawExponent),
  }
}

export function roundUpPaymentProduct(amount: number, multiplier: number): number {
  if (!Number.isFinite(amount) || !Number.isFinite(multiplier)) return 0

  const left = decimalInteger(amount)
  const right = decimalInteger(multiplier)
  const coefficient = left.coefficient * right.coefficient
  const scale = left.scale + right.scale
  let cents: bigint
  if (scale <= 2) {
    cents = coefficient * (10n ** BigInt(2 - scale))
  } else {
    const divisor = 10n ** BigInt(scale - 2)
    cents = coefficient / divisor
    if (coefficient % divisor !== 0n) cents += 1n
  }

  const rounded = Number(cents) / 100
  return (amount < 0) !== (multiplier < 0) ? -rounded : rounded
}

export function formatPaymentAmount(amount: number, currency?: string | null, locale?: string): string {
  const normalized = normalizePaymentCurrency(currency)
  const fractionDigits = paymentCurrencyFractionDigits(normalized)
  try {
    return new Intl.NumberFormat(locale || undefined, {
      style: 'currency',
      currency: normalized,
      currencyDisplay: 'narrowSymbol',
      minimumFractionDigits: fractionDigits,
      maximumFractionDigits: fractionDigits,
    }).format(Number.isFinite(amount) ? amount : 0)
  } catch {
    return `${normalized} ${(Number.isFinite(amount) ? amount : 0).toFixed(fractionDigits)}`
  }
}
