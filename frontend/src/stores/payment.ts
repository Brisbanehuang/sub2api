/**
 * Payment Store
 * Manages payment configuration, current order state, and subscription plans
 */

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { paymentAPI } from '@/api/payment'
import type { PaymentConfig, PaymentOrder, SubscriptionPlan, CreateOrderRequest } from '@/types/payment'

function paymentOrderRequestFingerprint(params: CreateOrderRequest): string {
  const normalized = Object.keys(params)
    .sort()
    .reduce<Record<string, unknown>>((result, key) => {
      const value = params[key as keyof CreateOrderRequest]
      if (value !== undefined) result[key] = value
      return result
    }, {})
  return JSON.stringify(normalized)
}

function newPaymentOrderIdempotencyKey(): string {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return `payment-order-${globalThis.crypto.randomUUID()}`
  }
  return `payment-order-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`
}

const BALANCE_PAY_PENDING_ORDER_STORAGE_KEY = 'payment.balance-pay.pending-order'
const BALANCE_PAY_PENDING_ORDER_TTL_MS = 24 * 60 * 60 * 1000

interface PendingBalancePayOrder {
  key: string
  userId: number
  createdAt: number
  expiresAt: number
}

type PendingBalancePayOrders = Record<string, PendingBalancePayOrder>

let inMemoryPendingBalancePayOrders: PendingBalancePayOrders = {}
// Failed storage deletion must not let an old persisted snapshot resurrect a completed key.
const pendingBalancePayOrderTombstones = new Set<string>()

function pendingBalancePayOrderLedgerKey(userId: number, fingerprint: string): string {
  return JSON.stringify([userId, fingerprint])
}

function normalizePendingBalancePayOrders(value: unknown): PendingBalancePayOrders {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}

  const pending: PendingBalancePayOrders = {}
  for (const [ledgerKey, rawRecord] of Object.entries(value)) {
    if (!rawRecord || typeof rawRecord !== 'object' || Array.isArray(rawRecord)) continue
    let ledgerOwner: unknown
    let ledgerFingerprint: unknown
    try {
      const parsedLedgerKey = JSON.parse(ledgerKey) as unknown
      if (!Array.isArray(parsedLedgerKey) || parsedLedgerKey.length !== 2) continue
      ;[ledgerOwner, ledgerFingerprint] = parsedLedgerKey
    } catch {
      continue
    }
    const record = rawRecord as Partial<PendingBalancePayOrder>
    if (
      typeof record.key !== 'string' || !record.key ||
      typeof record.userId !== 'number' || !Number.isInteger(record.userId) || record.userId <= 0 ||
      ledgerOwner !== record.userId || typeof ledgerFingerprint !== 'string' || !ledgerFingerprint ||
      typeof record.createdAt !== 'number' || !Number.isFinite(record.createdAt) ||
      typeof record.expiresAt !== 'number' || !Number.isFinite(record.expiresAt)
    ) {
      continue
    }
    pending[ledgerKey] = {
      key: record.key,
      userId: record.userId,
      createdAt: record.createdAt,
      expiresAt: record.expiresAt,
    }
  }
  return pending
}

function readPendingBalancePayOrders(legacyMigrationUserId?: number): PendingBalancePayOrders {
  if (typeof window === 'undefined') return { ...inMemoryPendingBalancePayOrders }
  try {
    const raw = window.sessionStorage.getItem(BALANCE_PAY_PENDING_ORDER_STORAGE_KEY)
    if (!raw) {
      return { ...inMemoryPendingBalancePayOrders }
    }
    const parsed = JSON.parse(raw) as unknown
    const persisted = normalizePendingBalancePayOrders(parsed)
    // Legacy records have no owner field, so migrate the complete snapshot only after validating the current user.
    if (legacyMigrationUserId && parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      for (const [legacyFingerprint, legacyKey] of Object.entries(parsed)) {
        if (typeof legacyKey !== 'string' || !legacyKey) continue
        const ledgerKey = pendingBalancePayOrderLedgerKey(legacyMigrationUserId, legacyFingerprint)
        if (!persisted[ledgerKey]) {
          const createdAt = Date.now()
          persisted[ledgerKey] = {
            key: legacyKey,
            userId: legacyMigrationUserId,
            createdAt,
            expiresAt: createdAt + BALANCE_PAY_PENDING_ORDER_TTL_MS,
          }
        }
      }
    }
    inMemoryPendingBalancePayOrders = { ...persisted, ...inMemoryPendingBalancePayOrders }
    for (const ledgerKey of pendingBalancePayOrderTombstones) {
      delete inMemoryPendingBalancePayOrders[ledgerKey]
    }
    return { ...inMemoryPendingBalancePayOrders }
  } catch {
    return { ...inMemoryPendingBalancePayOrders }
  }
}

function writePendingBalancePayOrders(pending: PendingBalancePayOrders): void {
  inMemoryPendingBalancePayOrders = { ...pending }
  if (typeof window === 'undefined') return
  try {
    if (Object.keys(pending).length === 0) {
      window.sessionStorage.removeItem(BALANCE_PAY_PENDING_ORDER_STORAGE_KEY)
    } else {
      window.sessionStorage.setItem(BALANCE_PAY_PENDING_ORDER_STORAGE_KEY, JSON.stringify(pending))
    }
    pendingBalancePayOrderTombstones.clear()
  } catch {
    // Keep the authoritative in-memory snapshot when storage is unavailable.
  }
}

function clearPendingBalancePayOrder(userId: number, fingerprint: string, idempotencyKey: string): void {
  const pending = readPendingBalancePayOrders()
  const ledgerKey = pendingBalancePayOrderLedgerKey(userId, fingerprint)
  const record = pending[ledgerKey]
  if (!record || record.userId !== userId || record.key !== idempotencyKey) return
  delete pending[ledgerKey]
  pendingBalancePayOrderTombstones.add(ledgerKey)
  writePendingBalancePayOrders(pending)
}

function isDefinitiveCreateOrderError(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const value = error as { status?: unknown; reason?: unknown }
  if (value.reason === 'IDEMPOTENCY_IN_PROGRESS' || value.reason === 'IDEMPOTENCY_RETRY_BACKOFF') return false
  const status = Number(value.status)
  return Number.isFinite(status) && status >= 400 && status < 500
}

export const usePaymentStore = defineStore('payment', () => {
  // ==================== State ====================

  /** Payment configuration from backend */
  const config = ref<PaymentConfig | null>(null)
  /** Currently active order (for payment flow) */
  const currentOrder = ref<PaymentOrder | null>(null)
  /** Available subscription plans */
  const plans = ref<SubscriptionPlan[]>([])

  const configLoading = ref(false)
  const configLoaded = ref(false)

  // ==================== Actions ====================

  /** Fetch payment configuration */
  async function fetchConfig(force = false): Promise<PaymentConfig | null> {
    if (configLoaded.value && !force) return config.value
    if (configLoading.value) return config.value

    configLoading.value = true
    try {
      const response = await paymentAPI.getConfig()
      config.value = response.data
      configLoaded.value = true
      return config.value
    } catch (error: unknown) {
      console.error('[payment] Failed to fetch config:', error)
      return null
    } finally {
      configLoading.value = false
    }
  }

  /** Fetch available subscription plans */
  async function fetchPlans(): Promise<SubscriptionPlan[]> {
    try {
      const response = await paymentAPI.getPlans()
      // Backend returns features as newline-separated string; parse to array
      plans.value = (response.data || []).map((p: Omit<SubscriptionPlan, 'features'> & { features: string | string[] }) => ({
        ...p,
        features: typeof p.features === 'string'
          ? p.features.split('\n').map((f: string) => f.trim()).filter(Boolean)
          : (p.features || []),
      }))
      return plans.value
    } catch (error: unknown) {
      console.error('[payment] Failed to fetch plans:', error)
      return []
    }
  }

  /** Create a new order and set it as current */
  async function createOrder(params: CreateOrderRequest, balancePayUserId?: number) {
    if (params.payment_type !== 'balance_pay') {
      const response = await paymentAPI.createOrder(params)
      return response.data
    }
    if (!Number.isInteger(balancePayUserId) || Number(balancePayUserId) <= 0) {
      throw new Error('[payment] balance_pay requires an authenticated user')
    }

    const fingerprint = paymentOrderRequestFingerprint(params)
    const userId = balancePayUserId as number
    const ledgerKey = pendingBalancePayOrderLedgerKey(userId, fingerprint)
    const pending = readPendingBalancePayOrders(userId)
    const existing = pending[ledgerKey]
    // An expired-but-uncertain result still reuses its durable key; expiry is metadata, not eviction.
    const idempotencyKey = existing?.key || newPaymentOrderIdempotencyKey()
    if (!existing) {
      pendingBalancePayOrderTombstones.delete(ledgerKey)
      const createdAt = Date.now()
      pending[ledgerKey] = {
        key: idempotencyKey,
        userId,
        createdAt,
        expiresAt: createdAt + BALANCE_PAY_PENDING_ORDER_TTL_MS,
      }
    }
    writePendingBalancePayOrders(pending)
    try {
      const response = await paymentAPI.createOrder(params, idempotencyKey)
      clearPendingBalancePayOrder(userId, fingerprint, idempotencyKey)
      return response.data
    } catch (error: unknown) {
      if (isDefinitiveCreateOrderError(error)) {
        clearPendingBalancePayOrder(userId, fingerprint, idempotencyKey)
      }
      throw error
    }
  }

  /** Poll order status by ID (read-only, no upstream check) */
  async function pollOrderStatus(orderId: number): Promise<PaymentOrder | null> {
    try {
      const response = await paymentAPI.getOrder(orderId)
      const order = response.data
      if (currentOrder.value?.id === orderId) {
        currentOrder.value = order
      }
      return order
    } catch (error: unknown) {
      console.error('[payment] Failed to poll order status:', error)
      return null
    }
  }

  /** Clear current order state */
  function clearCurrentOrder() {
    currentOrder.value = null
  }

  return {
    config,
    currentOrder,
    plans,
    configLoading,
    configLoaded,
    fetchConfig,
    fetchPlans,
    createOrder,
    pollOrderStatus,
    clearCurrentOrder
  }
})
