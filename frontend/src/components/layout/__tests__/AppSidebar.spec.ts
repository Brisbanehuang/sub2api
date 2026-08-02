import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { shallowMount, RouterLinkStub } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AppSidebar from '../AppSidebar.vue'

const appStore = vi.hoisted(() => ({
  backendModeEnabled: false,
  cachedPublicSettings: {},
  mobileOpen: false,
  publicSettingsLoaded: true,
  setMobileOpen: vi.fn(),
  sidebarCollapsed: false,
  sidebarScrollTop: 0,
  siteLogo: '',
  siteName: 'Sub2API',
  siteVersion: 'v0.1.151',
  toggleSidebar: vi.fn(),
}))

const authStore = vi.hoisted(() => ({
  isAdmin: false,
  isSimpleMode: false,
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/dashboard' }),
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/stores', () => ({
  useAdminSettingsStore: () => ({
    customMenuItems: [],
    fetch: vi.fn(),
    opsMonitoringEnabled: false,
    paymentEnabled: true,
  }),
  useAppStore: () => appStore,
  useAuthStore: () => authStore,
  useOnboardingStore: () => ({
    isCurrentStep: () => false,
    nextStep: vi.fn(),
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('@/composables/useBatchImageAccess', () => ({
  useBatchImageAccess: () => ({
    canUseBatchImage: { value: false },
    refreshBatchImageAccess: vi.fn().mockResolvedValue(false),
  }),
}))

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AppSidebar.vue')
const componentSource = readFileSync(componentPath, 'utf8')
const stylePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../style.css')
const styleSource = readFileSync(stylePath, 'utf8')

describe('AppSidebar custom SVG styles', () => {
  it('does not override uploaded SVG fill or stroke colors', () => {
    expect(componentSource).toContain('.sidebar-svg-icon {')
    expect(componentSource).toContain('color: currentColor;')
    expect(componentSource).toContain('display: block;')
    expect(componentSource).not.toContain('stroke: currentColor;')
    expect(componentSource).not.toContain('fill: none;')
  })
})

describe('AppSidebar scroll position persistence', () => {
  it('binds a template ref to the sidebar nav element', () => {
    expect(componentSource).toContain('ref="sidebarNavRef"')
    expect(componentSource).toContain('sidebar-nav')
  })

  it('declares sidebarNavRef in script setup', () => {
    expect(componentSource).toContain("const sidebarNavRef = ref<HTMLElement | null>(null)")
  })

  it('saves scroll position on beforeUnmount', () => {
    expect(componentSource).toContain('onBeforeUnmount')
    expect(componentSource).toContain('appStore.sidebarScrollTop')
    expect(componentSource).toContain('sidebarNavRef.value.scrollTop')
  })

  it('restores scroll position on mount', () => {
    expect(componentSource).toContain('onMounted')
    expect(componentSource).toContain('appStore.sidebarScrollTop')
    expect(componentSource).toContain('nextTick')
  })
})

describe('AppSidebar header styles', () => {
  it('does not clip the version badge dropdown', () => {
    const sidebarHeaderBlockMatch = styleSource.match(/\.sidebar-header\s*\{[\s\S]*?\n {2}\}/)
    const sidebarBrandBlockMatch = componentSource.match(/\.sidebar-brand\s*\{[\s\S]*?\n\}/)

    expect(sidebarHeaderBlockMatch).not.toBeNull()
    expect(sidebarBrandBlockMatch).not.toBeNull()
    expect(sidebarHeaderBlockMatch?.[0]).not.toContain('@apply overflow-hidden;')
    expect(sidebarBrandBlockMatch?.[0]).not.toContain('overflow: hidden;')
  })
})

describe('AppSidebar image Studio entry', () => {
  it('renders the image generator as real user navigation data', () => {
    const wrapper = shallowMount(AppSidebar, {
      global: {
        stubs: {
          RouterLink: RouterLinkStub,
          VersionBadge: true,
          Transition: false,
        },
      },
    })

    const studioLink = wrapper
      .findAllComponents(RouterLinkStub)
      .find(link => link.props('to') === '/image-generator')

    expect(studioLink).toBeDefined()
    expect(studioLink?.text()).toContain('nav.generateImages')
  })
})
