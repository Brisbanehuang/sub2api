<template>
  <AppLayout>
    <div class="mx-auto flex min-h-[60vh] max-w-2xl items-center justify-center px-4 py-12">
      <section class="card w-full p-8 text-center">
        <h1 class="text-2xl font-bold text-gray-900 dark:text-white">
          飞书集成
        </h1>
        <p class="mt-3 text-sm leading-6 text-gray-500 dark:text-gray-400">
          {{ statusText }}
        </p>
        <a class="btn btn-primary mt-6 inline-flex" :href="safeReturnTo" rel="noopener noreferrer">
          返回飞书集成配置
        </a>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'

const feishuUrl = 'https://feishu.brislouise.online'
const route = useRoute()
const statusText = ref('正在同步 OmniAPI 登录状态，即将返回飞书集成配置。')

const safeReturnTo = computed(() => {
  const value = String(route.query.return_to || '')
  return value.startsWith(`${feishuUrl}/`) ? value : `${feishuUrl}/config/feishu`
})

async function exchangeAndRedirect() {
  const token = localStorage.getItem('auth_token') || ''
  if (!token) {
    window.location.assign(safeReturnTo.value)
    return
  }

  try {
    await fetch(`${feishuUrl}/api/auth/sub2api/exchange`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ accessToken: token })
    })
  } catch {
    statusText.value = '登录状态同步失败，正在返回飞书集成配置页。'
  }
  window.location.assign(safeReturnTo.value)
}

onMounted(() => {
  exchangeAndRedirect()
})
</script>
