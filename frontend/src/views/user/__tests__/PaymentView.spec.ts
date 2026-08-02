import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, shallowMount } from '@vue/test-utils'
import PaymentView from '../PaymentView.vue'
import { PAYMENT_RECOVERY_STORAGE_KEY } from '@/components/payment/paymentFlow'
import { formatPaymentAmount } from '@/components/payment/currency'
import SubscriptionPlanCard from '@/components/payment/SubscriptionPlanCard.vue'
import type { CheckoutInfoResponse, MethodLimit, SubscriptionPlan } from '@/types/payment'

const routeState = vi.hoisted(() => ({
  path: '/purchase',
  query: {} as Record<string, unknown>,
}))

const routerReplace = vi.hoisted(() => vi.fn())
const routerPush = vi.hoisted(() => vi.fn())
const routerResolve = vi.hoisted(() => vi.fn(() => ({ href: '/payment/stripe?mock=1' })))
const createOrder = vi.hoisted(() => vi.fn())
const refreshUser = vi.hoisted(() => vi.fn())
const authUserState = vi.hoisted(() => ({
  id: 101,
  username: 'demo-user',
  balance: 0,
}))
const fetchActiveSubscriptions = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))
const showError = vi.hoisted(() => vi.fn())
const showInfo = vi.hoisted(() => vi.fn())
const showWarning = vi.hoisted(() => vi.fn())
const showSuccess = vi.hoisted(() => vi.fn())
const getCheckoutInfo = vi.hoisted(() => vi.fn())
const bridgeInvoke = vi.hoisted(() => vi.fn())

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({
      replace: routerReplace,
      push: routerPush,
      resolve: routerResolve,
    }),
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: authUserState,
    refreshUser,
  }),
}))

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({
    createOrder,
  }),
}))

vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({
    activeSubscriptions: [],
    fetchActiveSubscriptions,
  }),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
    showInfo,
    showWarning,
    showSuccess,
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: {},
    showError,
    showInfo,
    showWarning,
    showSuccess,
  }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    getCheckoutInfo,
  },
}))

vi.mock('@/utils/device', () => ({
  isMobileDevice: () => true,
}))

function checkoutInfoFixture(overrides: Partial<CheckoutInfoResponse> = {}) {
  const wxpayMethod: MethodLimit = {
    daily_limit: 0,
    daily_used: 0,
    daily_remaining: 0,
    single_min: 0,
    single_max: 0,
    fee_rate: 0,
    available: true,
  }
  const data: CheckoutInfoResponse = {
    methods: {
      wxpay: wxpayMethod,
    },
    global_min: 0,
    global_max: 0,
    plans: [],
    balance_disabled: false,
    balance_recharge_multiplier: 1,
    subscription_usd_to_cny_rate: 0,
    recharge_fee_rate: 0,
    help_text: '',
    help_image_url: '',
    stripe_publishable_key: '',
  }

  return {
    data: { ...data, ...overrides },
  }
}

function checkoutInfoWithPlansFixture(options: {
  checkout?: Partial<CheckoutInfoResponse>
  method?: Partial<MethodLimit>
  methods?: CheckoutInfoResponse['methods']
  plan?: Partial<SubscriptionPlan>
} = {}) {
  const base = checkoutInfoFixture(options.checkout).data
  const plan: SubscriptionPlan = {
    id: 7,
    group_id: 3,
    name: 'Starter',
    description: '',
    price: 128,
    original_price: 0,
    validity_days: 30,
    validity_unit: 'day',
    rate_multiplier: 1,
    daily_limit_usd: null,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    features: [],
    group_platform: 'openai',
    sort_order: 1,
    for_sale: true,
    group_name: 'OpenAI',
    ...options.plan,
  }

  return {
    data: {
      ...base,
      methods: options.methods ?? {
        ...base.methods,
        wxpay: {
          ...base.methods.wxpay,
          ...options.method,
        },
      },
      plans: [plan],
    },
  }
}

function checkoutInfoWithStripeFeeFixture() {
  return {
    data: {
      ...checkoutInfoFixture().data,
      methods: {
        balance_pay: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: 0,
          available: true,
        },
        usdt: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: 0,
          available: true,
        },
        stripe: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: 3,
          available: true,
        },
      },
    },
  }
}

function checkoutInfoWithBalancePayPlansFixture() {
  return {
    data: {
      ...checkoutInfoFixture().data,
      methods: {
        balance_pay: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: 0,
          available: true,
        },
        usdt: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: 0,
          available: true,
        },
        stripe: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: 3,
          available: true,
        },
      },
      plans: [
        {
          id: 35,
          group_id: 3,
          name: 'Starter',
          description: '',
          price: 35.9,
          original_price: 0,
          validity_days: 30,
          validity_unit: 'day',
          rate_multiplier: 1,
          daily_limit_usd: null,
          weekly_limit_usd: null,
          monthly_limit_usd: null,
          features: [],
          group_platform: 'openai',
          sort_order: 1,
          for_sale: true,
          group_name: 'OpenAI',
        },
      ],
      balance_recharge_multiplier: 1,
    },
  }
}

function jsapiOrderFixture(resumeToken: string) {
  return {
    order_id: 123,
    amount: 88,
    pay_amount: 88,
    fee_rate: 0,
    expires_at: '2099-01-01T00:10:00.000Z',
    payment_type: 'wxpay',
    out_trade_no: 'sub2_jsapi_123',
    result_type: 'jsapi_ready' as const,
    resume_token: resumeToken,
    jsapi: {
      appId: 'wx123',
      timeStamp: '1712345678',
      nonceStr: 'nonce',
      package: 'prepay_id=wx123',
      signType: 'RSA',
      paySign: 'signed',
    },
  }
}

function oauthOrderFixture() {
  return {
    order_id: 456,
    amount: 128,
    pay_amount: 128,
    fee_rate: 0,
    expires_at: '2099-01-01T00:10:00.000Z',
    payment_type: 'wxpay',
    result_type: 'oauth_required' as const,
    oauth: {
      authorize_url: '/api/v1/auth/oauth/wechat/payment/start?payment_type=wxpay&redirect=%2Fpurchase%3Ffrom%3Dwechat',
      appid: 'wx123',
      scope: 'snsapi_base',
      redirect_url: '/auth/wechat/payment/callback',
    },
  }
}

async function mountSubscriptionConfirm(options: Parameters<typeof checkoutInfoWithPlansFixture>[0] = {}) {
  vi.useRealTimers()
  routeState.path = '/purchase'
  routeState.query = {
    tab: 'subscription',
    group: '3',
  }
  routerReplace.mockReset().mockResolvedValue(undefined)
  routerPush.mockReset().mockResolvedValue(undefined)
  routerResolve.mockClear()
  createOrder.mockReset()
  refreshUser.mockReset()
  fetchActiveSubscriptions.mockReset().mockResolvedValue(undefined)
  showError.mockReset()
  showInfo.mockReset()
  showWarning.mockReset()
  getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoWithPlansFixture(options))
  bridgeInvoke.mockReset()
  window.localStorage.clear()
  ;(window as Window & { WeixinJSBridge?: { invoke: typeof bridgeInvoke } }).WeixinJSBridge = undefined

  const wrapper = shallowMount(PaymentView, {
    global: {
      stubs: {
        AppLayout: {
          template: '<div><slot /></div>',
        },
        Teleport: true,
        Transition: false,
      },
    },
  })
  await flushPromises()
  await flushPromises()
  return wrapper
}

async function mountSubscriptionPlanList(planCount: number) {
  vi.useRealTimers()
  routeState.path = '/purchase'
  routeState.query = { tab: 'subscription' }
  routerReplace.mockReset().mockResolvedValue(undefined)
  routerPush.mockReset().mockResolvedValue(undefined)
  routerResolve.mockClear()
  createOrder.mockReset()
  refreshUser.mockReset()
  fetchActiveSubscriptions.mockReset().mockResolvedValue(undefined)
  showError.mockReset()
  showInfo.mockReset()
  showWarning.mockReset()
  const basePlan = checkoutInfoWithPlansFixture().data.plans[0]
  const plans = Array.from({ length: planCount }, (_, index) => ({
    ...basePlan,
    id: index + 1,
    name: `Plan ${index + 1}`,
  }))
  getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoFixture({ plans }))
  bridgeInvoke.mockReset()
  window.localStorage.clear()
  ;(window as Window & { WeixinJSBridge?: { invoke: typeof bridgeInvoke } }).WeixinJSBridge = undefined

  const wrapper = shallowMount(PaymentView, {
    global: {
      stubs: {
        AppLayout: {
          template: '<div><slot /></div>',
        },
        Teleport: true,
        Transition: false,
      },
    },
  })
  await flushPromises()
  await flushPromises()
  return wrapper
}

describe('PaymentView subscription plan grid', () => {
  it.each([3, 4, 6])('keeps %i plans on the existing mobile/tablet/desktop grid', async (planCount) => {
    const wrapper = await mountSubscriptionPlanList(planCount)
    const cards = wrapper.findAllComponents(SubscriptionPlanCard)

    expect(cards).toHaveLength(planCount)
    expect([...(cards[0].element.parentElement?.classList ?? [])]).toEqual(expect.arrayContaining([
      'grid',
      'grid-cols-1',
      'sm:grid-cols-2',
      'lg:grid-cols-3',
    ]))
  })
})

describe('PaymentView subscription confirmation amounts', () => {
  it('shows converted CNY pay amount using the subscription rate, not the balance multiplier', async () => {
    const wrapper = await mountSubscriptionConfirm({
      checkout: {
        balance_recharge_multiplier: 0.14,
        subscription_usd_to_cny_rate: 7.15,
      },
      method: {
        currency: 'CNY',
      },
      plan: {
        price: 9.99,
        original_price: 12.99,
      },
    })

    const text = wrapper.text()
    const convertedPrice = formatPaymentAmount(71.43, 'CNY')
    const convertedOriginalPrice = formatPaymentAmount(92.88, 'CNY')

    expect(text).toContain(convertedPrice)
    expect(text).toContain(convertedOriginalPrice)
    expect(text).not.toContain(formatPaymentAmount(9.99, 'CNY'))
    // 换算必须使用订阅汇率（×7.15），而不是余额倍率（÷0.14 = 71.36）
    expect(text).not.toContain(formatPaymentAmount(71.36, 'CNY'))
    expect(wrapper.findAll('button').some(button => button.text().includes(convertedPrice))).toBe(true)
  })

  it('keeps plan price when the subscription rate is not configured or payment currency is not CNY', async () => {
    // opt-in 回归锁：即使余额倍率已配置，未配置订阅汇率时 CNY 订阅仍按 price 直付
    const cnyWrapper = await mountSubscriptionConfirm({
      checkout: {
        balance_recharge_multiplier: 0.14,
        subscription_usd_to_cny_rate: 0,
      },
      method: {
        currency: 'CNY',
      },
      plan: {
        price: 7.99,
      },
    })

    expect(cnyWrapper.text()).toContain(formatPaymentAmount(7.99, 'CNY'))
    expect(cnyWrapper.text()).not.toContain(formatPaymentAmount(57.07, 'CNY'))
    expect(cnyWrapper.text()).not.toContain(formatPaymentAmount(57.13, 'CNY'))

    const usdWrapper = await mountSubscriptionConfirm({
      checkout: {
        subscription_usd_to_cny_rate: 7.15,
      },
      method: {
        currency: 'USD',
      },
      plan: {
        price: 7.99,
        original_price: 9.99,
      },
    })

    expect(usdWrapper.text()).toContain(formatPaymentAmount(7.99, 'USD'))
    expect(usdWrapper.text()).toContain(formatPaymentAmount(9.99, 'USD'))
  })

  it('adds fee rate after CNY rate conversion to match backend pay_amount', async () => {
    const wrapper = await mountSubscriptionConfirm({
      checkout: {
        subscription_usd_to_cny_rate: 7.15,
        recharge_fee_rate: 2.5,
      },
      method: {
        currency: 'CNY',
      },
      plan: {
        price: 9.99,
      },
    })

    const text = wrapper.text()
    const convertedPrice = formatPaymentAmount(71.43, 'CNY')
    const fee = formatPaymentAmount(1.79, 'CNY')
    const total = formatPaymentAmount(73.22, 'CNY')

    expect(text).toContain(convertedPrice)
    expect(text).toContain(fee)
    expect(text).toContain(total)
    expect(wrapper.findAll('button').some(button => button.text().includes(total))).toBe(true)
  })

  it('uses each subscription method fee when checking limits and blocks an over-limit submit', async () => {
    const baseMethod = checkoutInfoFixture().data.methods.wxpay
    const wrapper = await mountSubscriptionConfirm({
      methods: {
        usdt: {
          ...baseMethod,
          single_max: 100,
        },
        stripe: {
          ...baseMethod,
          single_max: 100,
          fee_rate: 3,
        },
      },
      plan: {
        price: 100,
      },
    })
    const view = wrapper.vm as unknown as {
      subMethodOptions: Array<{ type: string; available: boolean }>
      selectedMethod: string
      confirmSubscribe: () => Promise<void>
    }

    expect(view.subMethodOptions.find(method => method.type === 'usdt')?.available).toBe(true)
    expect(view.subMethodOptions.find(method => method.type === 'stripe')?.available).toBe(false)

    view.selectedMethod = 'stripe'
    await wrapper.vm.$nextTick()
    const submitButton = wrapper.findAll('button').find(button => button.text().includes('payment.createOrder'))
    expect(submitButton?.attributes('disabled')).toBeDefined()
    await submitButton!.trigger('click')
    await view.confirmSubscribe()
    expect(createOrder).not.toHaveBeenCalled()
  })
})

describe('PaymentView payment recovery', () => {
  beforeEach(() => {
    vi.useRealTimers()
    routeState.path = '/purchase'
    routeState.query = {}
    routerReplace.mockReset().mockResolvedValue(undefined)
    routerPush.mockReset().mockResolvedValue(undefined)
    routerResolve.mockClear()
    createOrder.mockReset()
    refreshUser.mockReset()
    fetchActiveSubscriptions.mockReset().mockResolvedValue(undefined)
    showError.mockReset()
    showInfo.mockReset()
    showWarning.mockReset()
    bridgeInvoke.mockReset()
    window.localStorage.clear()
    ;(window as Window & { WeixinJSBridge?: { invoke: typeof bridgeInvoke } }).WeixinJSBridge = undefined
  })

  it('restores a custom EasyPay method as the selected payment method', async () => {
    getCheckoutInfo.mockResolvedValue(checkoutInfoFixture({
      methods: {
        wxpay: checkoutInfoFixture().data.methods.wxpay,
        ldc: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: 0,
          available: true,
          display_name: 'LDC Pay',
        },
      },
    }))
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      orderId: 888,
      amount: 66,
      qrCode: 'ldc-qr',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'ldc',
      payUrl: 'https://pay.example.com/ldc',
      outTradeNo: 'sub2_ldc_888',
      clientSecret: '',
      intentId: '',
      currency: '',
      countryCode: '',
      paymentEnv: '',
      payAmount: 66,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: '',
      createdAt: Date.now(),
    }))

    const wrapper = shallowMount(PaymentView, {
      global: {
        stubs: {
          AppLayout: {
            template: '<div><slot /></div>',
          },
          PaymentStatusPanel: {
            template: '<button data-test="payment-done" @click="$emit(\'done\')" />',
          },
          PaymentMethodSelector: {
            props: ['selected'],
            template: '<div data-test="method-selector">{{ selected }}</div>',
          },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()
    await wrapper.find('[data-test="payment-done"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="method-selector"]').text()).toBe('ldc')
  })
})

describe('PaymentView WeChat JSAPI flow', () => {
  beforeEach(() => {
    authUserState.username = 'demo-user'
    authUserState.balance = 0
    routeState.path = '/purchase'
    routeState.query = {
      wechat_resume: '1',
      wechat_resume_token: 'resume-token-123',
    }
    routerReplace.mockReset().mockResolvedValue(undefined)
    routerPush.mockReset().mockResolvedValue(undefined)
    routerResolve.mockClear()
    createOrder.mockReset()
    refreshUser.mockReset()
    fetchActiveSubscriptions.mockReset().mockResolvedValue(undefined)
    showError.mockReset()
    showInfo.mockReset()
    showWarning.mockReset()
    showSuccess.mockReset()
    getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoFixture())
    bridgeInvoke.mockReset()
    window.localStorage.clear()
    ;(window as Window & { WeixinJSBridge?: { invoke: typeof bridgeInvoke } }).WeixinJSBridge = {
      invoke: bridgeInvoke,
    }
  })

  it('resets payment state and redirects to /payment/result after JSAPI reports success', async () => {
    createOrder.mockResolvedValue(jsapiOrderFixture('resume-token-123'))
    bridgeInvoke.mockImplementation((_action, _payload, callback) => {
      callback({ err_msg: 'get_brand_wcpay_request:ok' })
    })

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(routerReplace).toHaveBeenCalledWith({ path: '/purchase', query: {} })
    expect(routerPush).toHaveBeenCalledWith({
      path: '/payment/result',
      query: {
        order_id: '123',
        out_trade_no: 'sub2_jsapi_123',
        resume_token: 'resume-token-123',
      },
    })
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('resets payment state when JSAPI reports cancellation', async () => {
    createOrder.mockResolvedValue(jsapiOrderFixture('resume-token-cancel'))
    bridgeInvoke.mockImplementation((_action, _payload, callback) => {
      callback({ err_msg: 'get_brand_wcpay_request:cancel' })
    })

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(showInfo).toHaveBeenCalledWith('payment.qr.cancelled')
    expect(routerPush).not.toHaveBeenCalled()
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('clears stale recovery state when JSAPI never becomes available', async () => {
    vi.useFakeTimers()
    createOrder.mockResolvedValue(jsapiOrderFixture('resume-token-missing-bridge'))
    ;(window as Window & { WeixinJSBridge?: { invoke: typeof bridgeInvoke } }).WeixinJSBridge = undefined

    const wrapper = shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })

    await flushPromises()
    await vi.advanceTimersByTimeAsync(4000)
    await flushPromises()
    await flushPromises()

    expect(showError).toHaveBeenCalledWith(
      'payment.errors.wechatJsapiUnavailable payment.errors.wechatOpenInWeChatHint',
    )
    expect(routerPush).not.toHaveBeenCalled()
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
    expect(wrapper.html()).not.toContain('payment-status-panel-stub')
  })

  it('clears a stale recovery snapshot before handling wechat resume callback params', async () => {
    createOrder.mockRejectedValueOnce(new Error('resume failed'))
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      orderId: 999,
      amount: 66,
      qrCode: 'stale-qr',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'alipay',
      payUrl: 'https://pay.example.com/stale',
      outTradeNo: 'stale-out-trade-no',
      clientSecret: '',
      intentId: '',
      currency: '',
      countryCode: '',
      paymentEnv: '',
      payAmount: 66,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: '',
      createdAt: Date.UTC(2099, 0, 1, 0, 0, 0),
    }))

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(createOrder).toHaveBeenCalledWith(expect.objectContaining({
      wechat_resume_token: 'resume-token-123',
    }))
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('keeps subscription resume context for token-only WeChat callbacks', async () => {
    routeState.query = {
      wechat_resume: '1',
      wechat_resume_token: 'resume-subscription-7',
      payment_type: 'wxpay_direct',
      order_type: 'subscription',
      plan_id: '7',
    }
    getCheckoutInfo.mockResolvedValue(checkoutInfoWithPlansFixture())
    createOrder.mockResolvedValue(oauthOrderFixture())

    const originalLocation = window.location
    const locationState = {
      href: 'http://localhost/purchase',
      origin: 'http://localhost',
    }
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState,
    })

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(routerReplace).toHaveBeenCalledWith({ path: '/purchase', query: {} })
    expect(createOrder).toHaveBeenCalledWith(expect.objectContaining({
      payment_type: 'wxpay',
      order_type: 'subscription',
      plan_id: 7,
      wechat_resume_token: 'resume-subscription-7',
    }))
    expect(locationState.href).toContain('/api/v1/auth/oauth/wechat/payment/start?')
    expect(new URL(locationState.href, 'http://localhost').searchParams.get('redirect')).toBe(
      '/purchase?from=wechat&payment_type=wxpay&order_type=subscription&plan_id=7',
    )

    Object.defineProperty(window, 'location', {
      configurable: true,
      value: originalLocation,
    })
  })

  it('falls back to QR flow when mobile WeChat payment is unavailable', async () => {
    routeState.query = {
      wechat_resume: '1',
      wechat_resume_token: 'resume-token-h5',
      payment_type: 'wxpay_direct',
    }
    createOrder
      .mockRejectedValueOnce({ reason: 'WECHAT_H5_NOT_AUTHORIZED' })
      .mockResolvedValueOnce({
        order_id: 778,
        amount: 88,
        pay_amount: 88,
        fee_rate: 0,
        expires_at: '2099-01-01T00:10:00.000Z',
        payment_type: 'wxpay',
        qr_code: 'weixin://wxpay/bizpayurl?pr=fallback-native',
        out_trade_no: 'sub2_qr_778',
      })

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(createOrder).toHaveBeenNthCalledWith(1, expect.objectContaining({
      payment_type: 'wxpay',
      is_mobile: true,
      wechat_resume_token: 'resume-token-h5',
    }))
    expect(createOrder).toHaveBeenNthCalledWith(2, expect.objectContaining({
      payment_type: 'wxpay',
      is_mobile: false,
      payment_source: 'hosted_redirect',
    }))
    expect(showWarning).toHaveBeenCalledWith('payment.errors.mobilePaymentFallbackToQr')
    expect(showError).not.toHaveBeenCalled()
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toContain('weixin://wxpay/bizpayurl?pr=fallback-native')
  })
})

describe('PaymentView Stripe fee preview', () => {
  beforeEach(() => {
    authUserState.username = 'demo-user'
    authUserState.balance = 0
    routeState.path = '/purchase'
    routeState.query = {}
    routerReplace.mockReset().mockResolvedValue(undefined)
    routerPush.mockReset().mockResolvedValue(undefined)
    routerResolve.mockClear()
    createOrder.mockReset()
    refreshUser.mockReset()
    fetchActiveSubscriptions.mockReset().mockResolvedValue(undefined)
    showError.mockReset()
    showInfo.mockReset()
    showWarning.mockReset()
    showSuccess.mockReset()
    getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoWithStripeFeeFixture())
    bridgeInvoke.mockReset()
    window.localStorage.clear()
  })

  it('shows 3 percent Stripe fee and 51.50 total for a 50 recharge', async () => {
    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    ;(wrapper.vm as unknown as { amount: number | null; selectedMethod: string }).amount = 50
    ;(wrapper.vm as unknown as { amount: number | null; selectedMethod: string }).selectedMethod = 'stripe'
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('payment.fee (3%)')
    expect(wrapper.text()).toContain('¥1.50')
    expect(wrapper.text()).toContain('¥51.50')
    expect(wrapper.text()).toContain('payment.createOrder ¥51.50')
  })

  it('rounds a 3 percent fee for 37 to 1.11 like the backend decimal calculation', async () => {
    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    ;(wrapper.vm as unknown as { amount: number | null; selectedMethod: string }).amount = 37
    ;(wrapper.vm as unknown as { amount: number | null; selectedMethod: string }).selectedMethod = 'stripe'
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('payment.fee (3%)')
    expect(wrapper.text()).toContain('¥1.11')
    expect(wrapper.text()).toContain('¥38.11')
    expect(wrapper.text()).toContain('payment.createOrder ¥38.11')
    expect(wrapper.text()).not.toContain('¥1.12')
    expect(wrapper.text()).not.toContain('¥38.12')
  })

  it('uses the backend three-decimal currency precision for IQD fees', async () => {
    const checkout = checkoutInfoWithStripeFeeFixture()
    checkout.data.methods.stripe.currency = 'IQD'
    getCheckoutInfo.mockResolvedValue(checkout)
    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    ;(wrapper.vm as unknown as { amount: number | null; selectedMethod: string }).amount = 37
    ;(wrapper.vm as unknown as { amount: number | null; selectedMethod: string }).selectedMethod = 'stripe'
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('1.110')
    expect(wrapper.text()).toContain('38.110')
  })

  it('uses each recharge method fee when checking candidate availability', async () => {
    const checkout = checkoutInfoWithStripeFeeFixture()
    checkout.data.methods.usdt.single_max = 100
    checkout.data.methods.stripe.single_max = 100
    getCheckoutInfo.mockResolvedValue(checkout)

    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    ;(wrapper.vm as unknown as { amount: number | null }).amount = 100
    await wrapper.vm.$nextTick()

    const usdtButton = wrapper.findAll('button').find(button => button.text().includes('payment.methods.usdt'))
    const stripeButton = wrapper.findAll('button').find(button => button.text().includes('payment.methods.stripe'))
    expect(usdtButton?.attributes('disabled')).toBeUndefined()
    expect(stripeButton?.attributes('disabled')).toBeDefined()
  })

  it('blocks a recharge submit when the selected method fee exceeds its limit', async () => {
    const checkout = checkoutInfoWithStripeFeeFixture()
    checkout.data.methods = {
      stripe: {
        ...checkout.data.methods.stripe,
        single_max: 100,
      },
    }
    getCheckoutInfo.mockResolvedValue(checkout)

    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    ;(wrapper.vm as unknown as { amount: number | null }).amount = 100
    await wrapper.vm.$nextTick()
    const submitButton = wrapper.findAll('button').find(button => button.text().includes('payment.createOrder'))
    expect(submitButton?.attributes('disabled')).toBeDefined()
    await submitButton!.trigger('click')
    expect(createOrder).not.toHaveBeenCalled()
  })
})

describe('PaymentView balance pay subscription flow', () => {
  beforeEach(() => {
    authUserState.username = 'demo-user'
    authUserState.balance = 0
    routeState.path = '/purchase'
    routeState.query = {}
    routerReplace.mockReset().mockResolvedValue(undefined)
    routerPush.mockReset().mockResolvedValue(undefined)
    routerResolve.mockClear()
    createOrder.mockReset()
    refreshUser.mockReset().mockResolvedValue(undefined)
    fetchActiveSubscriptions.mockReset().mockResolvedValue(undefined)
    showError.mockReset()
    showInfo.mockReset()
    showWarning.mockReset()
    showSuccess.mockReset()
    getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoWithBalancePayPlansFixture())
    bridgeInvoke.mockReset()
    window.localStorage.clear()
  })

  async function mountAndSelectSubscriptionPlan() {
    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    ;(wrapper.vm as unknown as { activeTab: 'subscription' }).activeTab = 'subscription'
    await wrapper.vm.$nextTick()
    const subscribeButton = wrapper.findAll('button').find((button) => button.text().includes('payment.subscribeNow'))
    expect(subscribeButton).toBeTruthy()
    await subscribeButton!.trigger('click')
    await flushPromises()
    await wrapper.vm.$nextTick()
    return wrapper
  }

  it('shows selectable balance pay when balance is enough and submits a subscription order', async () => {
    authUserState.balance = 100
    createOrder.mockResolvedValue({
      order_id: 881,
      amount: 35.9,
      pay_amount: 35.9,
      fee_rate: 0,
      expires_at: '2099-01-01T00:10:00.000Z',
      payment_type: 'balance_pay',
      status: 'COMPLETED',
    })

    const wrapper = await mountAndSelectSubscriptionPlan()

    expect(wrapper.text()).toContain('payment.methods.balance_pay')
    expect(wrapper.findAll('button').filter((button) => button.text().includes('payment.methods.balance_pay'))).toHaveLength(1)
    const balanceButton = wrapper.findAll('button').find((button) => button.text().includes('payment.methods.balance_pay'))
    expect(balanceButton?.attributes('disabled')).toBeUndefined()

    await balanceButton!.trigger('click')
    const submitButton = wrapper.findAll('button').find((button) => button.text().includes('payment.createOrder'))
    expect(submitButton?.attributes('disabled')).toBeUndefined()
    await submitButton!.trigger('click')
    await flushPromises()

    expect(createOrder).toHaveBeenCalledWith(expect.objectContaining({
      payment_type: 'balance_pay',
      order_type: 'subscription',
      plan_id: 35,
    }), 101)
  })

  it.each([
    ['user refresh', () => refreshUser.mockRejectedValueOnce(new Error('user refresh failed'))],
    ['subscription refresh', () => fetchActiveSubscriptions.mockRejectedValueOnce(new Error('subscription refresh failed'))],
  ])('keeps a completed balance pay purchase successful when %s fails', async (_label, failRefresh) => {
    authUserState.balance = 100
    createOrder.mockResolvedValue({
      order_id: 881,
      amount: 35.9,
      pay_amount: 35.9,
      fee_rate: 0,
      expires_at: '2099-01-01T00:10:00.000Z',
      payment_type: 'balance_pay',
      status: 'COMPLETED',
    })

    const wrapper = await mountAndSelectSubscriptionPlan()
    failRefresh()

    const balanceButton = wrapper.findAll('button').find(button => button.text().includes('payment.methods.balance_pay'))
    await balanceButton!.trigger('click')
    const submitButton = wrapper.findAll('button').find(button => button.text().includes('payment.createOrder'))
    await submitButton!.trigger('click')
    await flushPromises()

    expect(refreshUser).toHaveBeenCalled()
    expect(fetchActiveSubscriptions).toHaveBeenCalledWith(true)
    expect((wrapper.vm as unknown as { selectedPlan: SubscriptionPlan | null }).selectedPlan).toBeNull()
    expect(showSuccess).toHaveBeenCalledWith('payment.balancePay.success')
    expect(showError).not.toHaveBeenCalled()
  })

  it('does not show balance pay on subscription confirm when checkout methods omit it', async () => {
    authUserState.balance = 100
    getCheckoutInfo.mockResolvedValue(checkoutInfoWithPlansFixture())

    const wrapper = await mountAndSelectSubscriptionPlan()

    expect(wrapper.text()).not.toContain('payment.methods.balance_pay')
    expect((wrapper.vm as unknown as { selectedMethod: string }).selectedMethod).not.toBe('balance_pay')
  })

  it('does not show backend balance_pay methods on the recharge tab', async () => {
    authUserState.balance = 100

    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).not.toContain('payment.methods.balance_pay')
    expect(wrapper.text()).toContain('payment.methods.usdt')
  })

  it('resets to an external payment method after cancelling balance pay subscription confirmation', async () => {
    authUserState.balance = 100

    const wrapper = await mountAndSelectSubscriptionPlan()

    expect((wrapper.vm as unknown as { selectedMethod: string }).selectedMethod).toBe('balance_pay')
    const cancelButton = wrapper.findAll('button').find((button) => button.text().includes('common.cancel'))
    await cancelButton!.trigger('click')
    await wrapper.vm.$nextTick()
    ;(wrapper.vm as unknown as { activeTab: 'recharge'; amount: number | null }).activeTab = 'recharge'
    ;(wrapper.vm as unknown as { activeTab: 'recharge'; amount: number | null }).amount = 50
    await wrapper.vm.$nextTick()

    expect((wrapper.vm as unknown as { selectedMethod: string }).selectedMethod).not.toBe('balance_pay')

    createOrder.mockResolvedValue({
      order_id: 882,
      amount: 50,
      pay_amount: 50,
      fee_rate: 0,
      expires_at: '2099-01-01T00:10:00.000Z',
      payment_type: 'usdt',
    })
    const submitButton = wrapper.findAll('button').find((button) => button.text().includes('payment.createOrder'))
    expect(submitButton?.attributes('disabled')).toBeUndefined()
    await submitButton!.trigger('click')
    await flushPromises()

    expect(createOrder).toHaveBeenCalledWith(expect.objectContaining({
      order_type: 'balance',
      payment_type: 'usdt',
    }))
  })

  it('shows the required balance on the balance pay submit button when multiplier is not one', async () => {
    authUserState.balance = 100
    getCheckoutInfo.mockResolvedValue({
      data: {
        ...checkoutInfoWithBalancePayPlansFixture().data,
        balance_recharge_multiplier: 2,
      },
    })

    const wrapper = await mountAndSelectSubscriptionPlan()

    const submitButton = wrapper.findAll('button').find((button) => button.text().includes('payment.createOrder'))
    expect(submitButton?.text()).toContain('$71.80')
  })

  it('rounds the required balance up to cents like the backend', async () => {
    authUserState.balance = 0.01
    const checkout = checkoutInfoWithBalancePayPlansFixture()
    checkout.data.plans[0].price = 0.49
    checkout.data.balance_recharge_multiplier = 0.01
    getCheckoutInfo.mockResolvedValue(checkout)

    const wrapper = await mountAndSelectSubscriptionPlan()

    const submitButton = wrapper.findAll('button').find((button) => button.text().includes('payment.createOrder'))
    expect(submitButton?.text()).toContain('$0.01')
    expect(submitButton?.attributes('disabled')).toBeUndefined()
  })

  it('does not auto-select balance pay when RoundUp makes the balance insufficient', async () => {
    authUserState.balance = 0.005
    const checkout = checkoutInfoWithBalancePayPlansFixture()
    checkout.data.plans[0].price = 0.49
    checkout.data.balance_recharge_multiplier = 0.01
    getCheckoutInfo.mockResolvedValue(checkout)

    const wrapper = await mountAndSelectSubscriptionPlan()

    expect((wrapper.vm as unknown as { selectedMethod: string }).selectedMethod).not.toBe('balance_pay')
    const submitButton = wrapper.findAll('button').find((button) => button.text().includes('payment.createOrder'))
    expect(submitButton?.attributes('disabled')).toBeUndefined()
  })

  it('disables balance pay and shows 余额 insufficient hint when balance is too low', async () => {
    authUserState.balance = 10

    const wrapper = await mountAndSelectSubscriptionPlan()

    expect(wrapper.text()).toContain('payment.methods.balance_pay')
    expect(wrapper.text()).toContain('payment.balancePay.insufficientShort')
    const balanceButton = wrapper.findAll('button').find((button) => button.text().includes('payment.methods.balance_pay'))
    expect(balanceButton?.attributes('disabled')).toBeDefined()

    const usdtButton = wrapper.findAll('button').find((button) => button.text().includes('payment.methods.usdt'))
    const stripeButton = wrapper.findAll('button').find((button) => button.text().includes('payment.methods.stripe'))
    expect(usdtButton?.attributes('disabled')).toBeUndefined()
    expect(stripeButton?.attributes('disabled')).toBeUndefined()
  })

  it('shows the explicit insufficient balance error even when user refresh fails', async () => {
    authUserState.balance = 100
    refreshUser.mockRejectedValueOnce(new Error('user refresh failed'))
    createOrder.mockRejectedValue({
      reason: 'INSUFFICIENT_BALANCE',
      metadata: {
        current_balance: '10.00',
        required_balance: '35.90',
        deficit: '25.90',
      },
    })

    const wrapper = await mountAndSelectSubscriptionPlan()

    const balanceButton = wrapper.findAll('button').find((button) => button.text().includes('payment.methods.balance_pay'))
    await balanceButton!.trigger('click')
    const submitButton = wrapper.findAll('button').find((button) => button.text().includes('payment.createOrder'))
    await submitButton!.trigger('click')
    await flushPromises()

    expect(refreshUser).toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith(expect.stringContaining('payment.balancePay.insufficient'))
    expect(showError).toHaveBeenCalledWith(expect.stringContaining('payment.balancePay.insufficientHint'))
  })
})
