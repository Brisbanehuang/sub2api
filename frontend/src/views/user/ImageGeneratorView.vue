<template>
  <AppLayout>
    <div class="mx-auto flex min-h-[60vh] max-w-5xl items-center justify-center px-4 py-12">
      <section class="card w-full p-8">
        <div class="mx-auto mb-5 flex h-14 w-14 items-center justify-center rounded-2xl bg-primary-50 text-primary-600 dark:bg-primary-900/20 dark:text-primary-300">
          <Icon name="sparkles" size="lg" />
        </div>
        <h1 class="text-center text-2xl font-bold text-gray-900 dark:text-white">
          {{ t('imageGenerator.title') }}
        </h1>
        <p class="mt-3 text-center text-sm leading-6 text-gray-500 dark:text-gray-400">
          {{ statusText }}
        </p>
        <div class="mt-7 rounded-lg border border-primary-200 bg-primary-50/40 p-5 dark:border-primary-800 dark:bg-primary-900/10">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t('imageGenerator.studioTitle') }}
          </h2>
          <p class="mt-2 text-sm leading-6 text-gray-500 dark:text-gray-400">
            {{ t('imageGenerator.studioDescription') }}
          </p>
          <button class="btn btn-primary mt-5 w-full justify-center" type="button" @click="exchangeAndOpen(STUDIO_URL)">
            {{ t('imageGenerator.studioButton') }}
          </button>
        </div>
        <p class="mt-5 rounded-lg bg-amber-50 px-4 py-3 text-sm leading-6 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
          {{ t('imageGenerator.retentionNotice') }}
        </p>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'

const STUDIO_URL = 'https://img.brislouise.online'
const { t } = useI18n()
const statusText = ref(t('imageGenerator.subtitle'))

async function exchangeAndOpen(targetUrl: string) {
  const token = localStorage.getItem('auth_token') || ''
  if (!token) {
    window.location.assign(targetUrl)
    return
  }
  statusText.value = t('imageGenerator.syncing')
  try {
    await fetch(`${targetUrl}/api/auth/sub2api/exchange`, {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ accessToken: token })
    })
  } catch {
    // Studio 会负责展示登录状态；这里失败时仍跳转，避免用户卡在中转页。
  }
  window.location.assign(targetUrl)
}
</script>
