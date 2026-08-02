import { describe, expect, it, vi } from 'vitest'

const authStore = vi.hoisted(() => ({
  checkAuth: vi.fn(),
  isAuthenticated: false,
  isAdmin: false,
  isSimpleMode: false,
}))

const appStore = vi.hoisted(() => ({
  siteName: 'Sub2API',
  backendModeEnabled: false,
  cachedPublicSettings: null as null | Record<string, unknown>,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authStore,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({
    customMenuItems: [],
  }),
}))

vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({
    startNavigation: vi.fn(),
    endNavigation: vi.fn(),
    isLoading: { value: false },
  }),
}))

vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => ({
    triggerPrefetch: vi.fn(),
    cancelPendingPrefetch: vi.fn(),
    resetPrefetchState: vi.fn(),
  }),
}))

describe('router WeChat OAuth route', () => {
  it('registers the WeChat callback route as a public route', async () => {
    const { default: router } = await import('@/router')
    const route = router.getRoutes().find((record) => record.name === 'WeChatOAuthCallback')

    expect(route?.path).toBe('/auth/wechat/callback')
    expect(route?.meta.requiresAuth).toBe(false)
    expect(route?.meta.title).toBe('WeChat OAuth Callback')
  })

  it('registers the WeChat payment callback route as a public route', async () => {
    const { default: router } = await import('@/router')
    const route = router.getRoutes().find((record) => record.name === 'WeChatPaymentOAuthCallback')

    expect(route?.path).toBe('/auth/wechat/payment/callback')
    expect(route?.meta.requiresAuth).toBe(false)
    expect(route?.meta.title).toBe('WeChat Payment Callback')
  })

  it('registers the custom bridge routes without payment or risk-control gates', async () => {
    const { default: router } = await import('@/router')
    const imageRoute = router.getRoutes().find((record) => record.name === 'ImageGenerator')
    const feishuRoute = router.getRoutes().find((record) => record.name === 'FeishuIntegration')

    expect(imageRoute).toMatchObject({
      path: '/image-generator',
      name: 'ImageGenerator',
      meta: {
        requiresAuth: true,
        requiresAdmin: false,
        title: 'Generate Images',
        titleKey: 'imageGenerator.title',
        descriptionKey: 'imageGenerator.subtitle',
      },
    })
    expect(imageRoute?.meta.requiresPayment).not.toBe(true)
    expect(imageRoute?.meta.requiresRiskControl).not.toBe(true)

    expect(feishuRoute).toMatchObject({
      path: '/feishu-integration',
      name: 'FeishuIntegration',
      meta: {
        requiresAuth: true,
        requiresAdmin: false,
        title: 'Feishu Integration',
      },
    })
    expect(feishuRoute?.meta.requiresPayment).not.toBe(true)
    expect(feishuRoute?.meta.requiresRiskControl).not.toBe(true)
  })
})
