import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get,
    post,
  },
}))

import { paymentAPI } from '@/api/payment'
import type { CreateOrderRequest } from '@/types/payment'

describe('payment api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    get.mockResolvedValue({ data: {} })
    post.mockResolvedValue({ data: {} })
  })

  it('keeps legacy public out_trade_no verification for upgrade compatibility', async () => {
    await paymentAPI.verifyOrderPublic('legacy-order-no')

    expect(post).toHaveBeenCalledWith('/payment/public/orders/verify', {
      out_trade_no: 'legacy-order-no',
    })
  })

  it('keeps signed public resume-token resolve endpoint', async () => {
    await paymentAPI.resolveOrderPublicByResumeToken('resume-token-123')

    expect(post).toHaveBeenCalledWith('/payment/public/orders/resolve', {
      resume_token: 'resume-token-123',
    })
  })

  it('sends an idempotency key when creating an order', async () => {
    const order: CreateOrderRequest = {
      amount: 35.9,
      payment_type: 'balance_pay',
      order_type: 'subscription',
      plan_id: 7,
    }

    await paymentAPI.createOrder(order, 'payment-order-key-123')

    expect(post).toHaveBeenCalledWith('/payment/orders', order, {
      headers: {
        'Idempotency-Key': 'payment-order-key-123',
      },
    })
  })

  it('keeps the existing external create-order request shape even when a key is supplied', async () => {
    const order: CreateOrderRequest = {
      amount: 10,
      payment_type: 'stripe',
      order_type: 'balance',
    }

    await paymentAPI.createOrder(order, 'must-not-be-sent')

    expect(post).toHaveBeenCalledWith('/payment/orders', order)
  })
})
