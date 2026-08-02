import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { usePaymentStore } from '@/stores/payment'
import type { CreateOrderRequest, CreateOrderResult } from '@/types/payment'

const createOrder = vi.hoisted(() => vi.fn())
const originalSessionStorageDescriptor = Object.getOwnPropertyDescriptor(window, 'sessionStorage')

function createMemoryStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() {
      return values.size
    },
    clear: () => values.clear(),
    getItem: (key: string) => values.get(key) ?? null,
    key: (index: number) => Array.from(values.keys())[index] ?? null,
    removeItem: (key: string) => {
      values.delete(key)
    },
    setItem: (key: string, value: string) => {
      values.set(key, value)
    },
  }
}

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    createOrder,
  },
}))

const payload: CreateOrderRequest = {
  amount: 35.9,
  payment_type: 'balance_pay',
  order_type: 'subscription',
  plan_id: 7,
}

const completedOrder: CreateOrderResult = {
  order_id: 81,
  amount: 35.9,
  pay_amount: 35.9,
  fee_rate: 0,
  expires_at: '2099-01-01T00:10:00.000Z',
  payment_type: 'balance_pay',
  status: 'COMPLETED',
}

describe('usePaymentStore create order idempotency', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    setActivePinia(createPinia())
    createOrder.mockReset()
    Object.defineProperty(window, 'sessionStorage', {
      configurable: true,
      value: createMemoryStorage(),
    })
  })

  afterEach(() => {
    vi.restoreAllMocks()
    if (originalSessionStorageDescriptor) {
      Object.defineProperty(window, 'sessionStorage', originalSessionStorageDescriptor)
    }
  })

  it.each([
    [{ status: 0, message: 'Network error' }, 'network error'],
    [{ status: 503, reason: 'SERVICE_UNAVAILABLE' }, '5xx response'],
    [{ status: 409, reason: 'IDEMPOTENCY_IN_PROGRESS' }, 'in-progress response'],
    [{ status: 409, reason: 'IDEMPOTENCY_RETRY_BACKOFF' }, 'retry-backoff response'],
  ])('reuses the persisted balance-pay key after %s (%s)', async (transientError) => {
    createOrder
      .mockRejectedValueOnce(transientError)
      .mockResolvedValueOnce({ data: completedOrder })

    const firstStore = usePaymentStore()
    await expect(firstStore.createOrder(payload, 101)).rejects.toMatchObject(transientError)
    const firstKey = createOrder.mock.calls[0]?.[1]
    expect(firstKey).toEqual(expect.any(String))
    expect(window.sessionStorage.length).toBe(1)

    setActivePinia(createPinia())
    const refreshedStore = usePaymentStore()
    await expect(refreshedStore.createOrder({ ...payload }, 101)).resolves.toEqual(completedOrder)
    const retriedKey = createOrder.mock.calls[1]?.[1]
    expect(retriedKey).toBe(firstKey)
    expect(window.sessionStorage.length).toBe(0)
  })

  it('clears the persisted key after success so an identical new purchase gets a new key', async () => {
    createOrder
      .mockResolvedValueOnce({ data: completedOrder })
      .mockResolvedValueOnce({ data: { ...completedOrder, order_id: 82 } })

    const store = usePaymentStore()
    await expect(store.createOrder(payload, 101)).resolves.toEqual(completedOrder)
    expect(window.sessionStorage.length).toBe(0)
    await expect(store.createOrder({ ...payload }, 101)).resolves.toMatchObject({ order_id: 82 })

    expect(createOrder.mock.calls[0]?.[1]).toEqual(expect.any(String))
    expect(createOrder.mock.calls[1]?.[1]).not.toBe(createOrder.mock.calls[0]?.[1])
    expect(window.sessionStorage.length).toBe(0)
  })

  it('uses a new key after a definitive server response', async () => {
    createOrder
      .mockRejectedValueOnce({ status: 400, reason: 'INSUFFICIENT_BALANCE' })
      .mockResolvedValueOnce({ data: completedOrder })

    const store = usePaymentStore()
    await expect(store.createOrder(payload, 101)).rejects.toMatchObject({ status: 400 })
    expect(window.sessionStorage.length).toBe(0)
    await expect(store.createOrder(payload, 101)).resolves.toEqual(completedOrder)

    expect(createOrder.mock.calls[0]?.[1]).toEqual(expect.any(String))
    expect(createOrder.mock.calls[1]?.[1]).not.toBe(createOrder.mock.calls[0]?.[1])
  })

  it('keeps external payment requests free of idempotency keys and session state', async () => {
    const externalPayload: CreateOrderRequest = {
      amount: 10,
      payment_type: 'stripe',
      order_type: 'balance',
    }
    createOrder.mockResolvedValueOnce({ data: { ...completedOrder, payment_type: 'stripe' } })

    const store = usePaymentStore()
    await store.createOrder(externalPayload)

    expect(createOrder).toHaveBeenCalledWith(externalPayload)
    expect(window.sessionStorage.length).toBe(0)
  })

  it('retains independent keys for multiple pending balance-pay intents', async () => {
    const otherPayload: CreateOrderRequest = {
      ...payload,
      plan_id: 8,
    }
    createOrder
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockRejectedValueOnce({ status: 503, reason: 'SERVICE_UNAVAILABLE' })
      .mockResolvedValueOnce({ data: completedOrder })

    const store = usePaymentStore()
    await expect(store.createOrder(payload, 101)).rejects.toMatchObject({ status: 0 })
    const firstIntentKey = createOrder.mock.calls[0]?.[1]
    await expect(store.createOrder(otherPayload, 101)).rejects.toMatchObject({ status: 503 })
    const secondIntentKey = createOrder.mock.calls[1]?.[1]

    setActivePinia(createPinia())
    const refreshedStore = usePaymentStore()
    await expect(refreshedStore.createOrder({ ...payload }, 101)).resolves.toEqual(completedOrder)

    expect(firstIntentKey).toEqual(expect.any(String))
    expect(secondIntentKey).toEqual(expect.any(String))
    expect(secondIntentKey).not.toBe(firstIntentKey)
    expect(createOrder.mock.calls[2]?.[1]).toBe(firstIntentKey)
    expect(window.sessionStorage.length).toBe(1)

    createOrder.mockResolvedValueOnce({ data: { ...completedOrder, order_id: 83 } })
    await refreshedStore.createOrder(otherPayload, 101)
  })

  it('persists exact recovery records and keeps identical intents isolated by user', async () => {
    createOrder
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockResolvedValueOnce({ data: completedOrder })
    vi.spyOn(Date, 'now').mockReturnValue(1_000)

    const firstUserStore = usePaymentStore()
    await expect(firstUserStore.createOrder(payload, 101)).rejects.toMatchObject({ status: 0 })
    const firstUserKey = createOrder.mock.calls[0]?.[1]

    setActivePinia(createPinia())
    const secondUserStore = usePaymentStore()
    await expect(secondUserStore.createOrder(payload, 202)).rejects.toMatchObject({ status: 0 })
    const secondUserKey = createOrder.mock.calls[1]?.[1]

    const stored = JSON.parse(window.sessionStorage.getItem('payment.balance-pay.pending-order') || '{}') as Record<string, unknown>
    expect(Object.values(stored)).toEqual(expect.arrayContaining([
      {
        key: firstUserKey,
        userId: 101,
        createdAt: 1_000,
        expiresAt: 86_401_000,
      },
      {
        key: secondUserKey,
        userId: 202,
        createdAt: 1_000,
        expiresAt: 86_401_000,
      },
    ]))
    expect(Object.values(stored).every(record => (
      Object.keys(record as Record<string, unknown>).sort().join(',') === 'createdAt,expiresAt,key,userId'
    ))).toBe(true)
    expect(secondUserKey).not.toBe(firstUserKey)

    setActivePinia(createPinia())
    await expect(usePaymentStore().createOrder(payload, 101)).resolves.toEqual(completedOrder)
    expect(createOrder.mock.calls[2]?.[1]).toBe(firstUserKey)

    const remaining = JSON.parse(window.sessionStorage.getItem('payment.balance-pay.pending-order') || '{}') as Record<string, unknown>
    expect(Object.values(remaining)).toEqual([expect.objectContaining({ userId: 202, key: secondUserKey })])

    createOrder.mockResolvedValueOnce({ data: { ...completedOrder, order_id: 83 } })
    await usePaymentStore().createOrder(payload, 202)
  })

  it('ignores a structured record whose ledger owner disagrees with its userId', async () => {
    const fingerprint = JSON.stringify({
      amount: 35.9,
      order_type: 'subscription',
      payment_type: 'balance_pay',
      plan_id: 7,
    })
    window.sessionStorage.setItem('payment.balance-pay.pending-order', JSON.stringify({
      [JSON.stringify([101, fingerprint])]: {
        key: 'payment-order-corrupt-owner',
        userId: 202,
        createdAt: 1_000,
        expiresAt: 86_401_000,
      },
    }))
    createOrder.mockResolvedValueOnce({ data: completedOrder })

    await expect(usePaymentStore().createOrder(payload, 101)).resolves.toEqual(completedOrder)

    expect(createOrder.mock.calls[0]?.[1]).not.toBe('payment-order-corrupt-owner')
  })

  it('reuses an expired recovery key while the previous result is unknown', async () => {
    createOrder
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockResolvedValueOnce({ data: completedOrder })
    const now = vi.spyOn(Date, 'now').mockReturnValue(5_000)

    await expect(usePaymentStore().createOrder(payload, 101)).rejects.toMatchObject({ status: 0 })
    const firstKey = createOrder.mock.calls[0]?.[1]
    now.mockReturnValue(86_405_001)

    setActivePinia(createPinia())
    await expect(usePaymentStore().createOrder(payload, 101)).resolves.toEqual(completedOrder)
    expect(createOrder.mock.calls[1]?.[1]).toBe(firstKey)
  })

  it('keeps the in-memory key when persisted session state disappears', async () => {
    createOrder
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockResolvedValueOnce({ data: completedOrder })

    await expect(usePaymentStore().createOrder(payload, 101)).rejects.toMatchObject({ status: 0 })
    const firstKey = createOrder.mock.calls[0]?.[1]
    window.sessionStorage.clear()

    setActivePinia(createPinia())
    await expect(usePaymentStore().createOrder(payload, 101)).resolves.toEqual(completedOrder)

    expect(createOrder.mock.calls[1]?.[1]).toBe(firstKey)
  })

  it('migrates every legacy recovery key for the current user before rewriting storage', async () => {
    const legacyFingerprint = JSON.stringify({
      amount: 35.9,
      order_type: 'subscription',
      payment_type: 'balance_pay',
      plan_id: 7,
    })
    const secondLegacyPayload: CreateOrderRequest = { ...payload, plan_id: 8 }
    const secondLegacyFingerprint = JSON.stringify({
      amount: 35.9,
      order_type: 'subscription',
      payment_type: 'balance_pay',
      plan_id: 8,
    })
    window.sessionStorage.setItem('payment.balance-pay.pending-order', JSON.stringify({
      [legacyFingerprint]: 'payment-order-legacy-key',
      [secondLegacyFingerprint]: 'payment-order-second-legacy-key',
    }))
    createOrder
      .mockResolvedValueOnce({ data: completedOrder })
      .mockResolvedValueOnce({ data: { ...completedOrder, order_id: 82 } })

    const store = usePaymentStore()
    await expect(store.createOrder(payload, 101)).resolves.toEqual(completedOrder)
    await expect(store.createOrder(secondLegacyPayload, 101)).resolves.toMatchObject({ order_id: 82 })

    expect(createOrder).toHaveBeenCalledWith(payload, 'payment-order-legacy-key')
    expect(createOrder).toHaveBeenCalledWith(secondLegacyPayload, 'payment-order-second-legacy-key')
    expect(window.sessionStorage.length).toBe(0)
  })

  it('falls back to in-memory recovery when sessionStorage throws', async () => {
    createOrder
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockResolvedValueOnce({ data: completedOrder })
    const getItem = vi.spyOn(window.sessionStorage, 'getItem').mockImplementation(() => {
      throw new Error('storage blocked')
    })
    const setItem = vi.spyOn(window.sessionStorage, 'setItem').mockImplementation(() => {
      throw new Error('storage blocked')
    })
    const removeItem = vi.spyOn(window.sessionStorage, 'removeItem').mockImplementation(() => {
      throw new Error('storage blocked')
    })

    await expect(usePaymentStore().createOrder(payload, 101)).rejects.toMatchObject({ status: 0 })
    const firstKey = createOrder.mock.calls[0]?.[1]

    setActivePinia(createPinia())
    await expect(usePaymentStore().createOrder(payload, 101)).resolves.toEqual(completedOrder)
    expect(createOrder.mock.calls[1]?.[1]).toBe(firstKey)
    expect(getItem).toHaveBeenCalled()
    expect(setItem).toHaveBeenCalled()
    expect(removeItem).toHaveBeenCalled()
  })

  it('does not resurrect a completed key when sessionStorage removal fails', async () => {
    createOrder
      .mockRejectedValueOnce({ status: 0, message: 'Network error' })
      .mockResolvedValueOnce({ data: completedOrder })
      .mockResolvedValueOnce({ data: { ...completedOrder, order_id: 82 } })
    const store = usePaymentStore()

    await expect(store.createOrder(payload, 101)).rejects.toMatchObject({ status: 0 })
    const completedKey = createOrder.mock.calls[0]?.[1]
    const removeItem = vi.spyOn(window.sessionStorage, 'removeItem').mockImplementation(() => {
      throw new Error('storage removal blocked')
    })

    await expect(store.createOrder(payload, 101)).resolves.toEqual(completedOrder)
    await expect(store.createOrder(payload, 101)).resolves.toMatchObject({ order_id: 82 })

    expect(removeItem).toHaveBeenCalled()
    expect(createOrder.mock.calls[1]?.[1]).toBe(completedKey)
    expect(createOrder.mock.calls[2]?.[1]).not.toBe(completedKey)

    removeItem.mockRestore()
    createOrder.mockResolvedValueOnce({ data: { ...completedOrder, order_id: 83 } })
    await store.createOrder(payload, 101)
  })

  it('does not resurrect a completed legacy key when migration and removal both fail', async () => {
    const legacyFingerprint = JSON.stringify({
      amount: 35.9,
      order_type: 'subscription',
      payment_type: 'balance_pay',
      plan_id: 7,
    })
    window.sessionStorage.setItem('payment.balance-pay.pending-order', JSON.stringify({
      [legacyFingerprint]: 'payment-order-legacy-key',
    }))
    vi.spyOn(window.sessionStorage, 'setItem').mockImplementation(() => {
      throw new Error('storage write blocked')
    })
    vi.spyOn(window.sessionStorage, 'removeItem').mockImplementation(() => {
      throw new Error('storage removal blocked')
    })
    createOrder
      .mockResolvedValueOnce({ data: completedOrder })
      .mockResolvedValueOnce({ data: { ...completedOrder, order_id: 82 } })

    const store = usePaymentStore()
    await expect(store.createOrder(payload, 101)).resolves.toEqual(completedOrder)
    await expect(store.createOrder(payload, 101)).resolves.toMatchObject({ order_id: 82 })

    expect(createOrder.mock.calls[0]?.[1]).toBe('payment-order-legacy-key')
    expect(createOrder.mock.calls[1]?.[1]).not.toBe('payment-order-legacy-key')
  })

  it.each([undefined, 0, -1, 1.5])('rejects invalid balance-pay user id %s before storage or API work', async (userId) => {
    const getItem = vi.spyOn(window.sessionStorage, 'getItem')
    await expect(usePaymentStore().createOrder(payload, userId)).rejects.toThrow('authenticated user')

    expect(createOrder).not.toHaveBeenCalled()
    expect(getItem).not.toHaveBeenCalled()
    expect(window.sessionStorage.length).toBe(0)
  })
})
