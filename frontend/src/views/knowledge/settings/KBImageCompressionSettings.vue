<template>
  <div class="image-compression-settings">
    <div class="section-header">
      <h2>{{ t('knowledgeEditor.imageCompression.title') }}</h2>
      <p class="section-description">{{ t('knowledgeEditor.imageCompression.description') }}</p>
    </div>

    <t-alert theme="info" :message="t('knowledgeEditor.imageCompression.scopeNote')" />

    <div v-if="loadingPreview" class="loading-state" role="status">
      {{ t('common.loading') }}
    </div>

    <template v-else-if="preview">
      <dl class="summary-grid">
        <div class="summary-item">
          <dt>{{ t('knowledgeEditor.imageCompression.eligible') }}</dt>
          <dd>{{ preview.eligible_images }}</dd>
        </div>
        <div class="summary-item">
          <dt>{{ t('knowledgeEditor.imageCompression.sourceSize') }}</dt>
          <dd>{{ formatBytes(preview.total_source_bytes) }}</dd>
        </div>
        <div class="summary-item">
          <dt>{{ t('knowledgeEditor.imageCompression.target') }}</dt>
          <dd>≤ {{ formatBytes(preview.target_bytes) }}</dd>
        </div>
      </dl>

      <div v-if="progress" class="progress-panel" role="status" aria-live="polite">
        <div class="progress-heading">
          <span>{{ t('knowledgeEditor.imageCompression.progress') }}</span>
          <span>{{ progressPercent }}%</span>
        </div>
        <t-progress :percentage="progressPercent" :status="progressStatus" />
        <p class="progress-message">{{ progress.message }}</p>
        <div class="progress-facts">
          <span>{{ t('knowledgeEditor.imageCompression.succeeded', { count: progress.succeeded }) }}</span>
          <span>{{ t('knowledgeEditor.imageCompression.retrying', { count: progress.retrying }) }}</span>
          <span>{{ t('knowledgeEditor.imageCompression.failed', { count: totalFailed }) }}</span>
          <span>{{ t('knowledgeEditor.imageCompression.saved', { size: formatBytes(progress.saved_bytes) }) }}</span>
        </div>
      </div>

      <div class="actions">
        <t-button variant="outline" :loading="loadingPreview" @click="loadPreview">
          {{ t('knowledgeEditor.imageCompression.refresh') }}
        </t-button>
        <t-button
          theme="primary"
          :disabled="preview.eligible_images === 0 || taskRunning"
          :loading="starting"
          @click="confirmStart"
        >
          {{ taskRunning ? t('knowledgeEditor.imageCompression.running') : t('knowledgeEditor.imageCompression.start') }}
        </t-button>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  getKnowledgeImageCompressionProgress,
  previewKnowledgeImageCompression,
  startKnowledgeImageCompression,
  type KnowledgeImageCompressionPreview,
  type KnowledgeImageCompressionProgress,
} from '@/api/knowledge-base'

const props = defineProps<{ kbId: string }>()
const { t } = useI18n()
const preview = ref<KnowledgeImageCompressionPreview | null>(null)
const progress = ref<KnowledgeImageCompressionProgress | null>(null)
const loadingPreview = ref(false)
const starting = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | undefined

const taskRunning = computed(() => progress.value?.status === 'pending' || progress.value?.status === 'processing')
const totalFailed = computed(() => (progress.value?.failed || 0) + (progress.value?.permanent_failed || 0))
const progressPercent = computed(() => {
  if (!progress.value?.total) return progress.value?.status === 'completed' ? 100 : 0
  const done = progress.value.succeeded + progress.value.skipped + totalFailed.value
  return Math.min(100, Math.round(done * 100 / progress.value.total))
})
const progressStatus = computed(() => progress.value?.status === 'failed' ? 'error' : 'active')

function unwrap<T>(response: any): T {
  return (response?.data ?? response) as T
}

function formatBytes(value: number): string {
  if (!value) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toFixed(index === 0 ? 0 : 1)} ${units[index]}`
}

async function loadPreview() {
  loadingPreview.value = true
  try {
    preview.value = unwrap<KnowledgeImageCompressionPreview>(await previewKnowledgeImageCompression(props.kbId))
  } catch (error) {
    console.error('Failed to preview knowledge image compression:', error)
    MessagePlugin.error(t('knowledgeEditor.imageCompression.previewFailed'))
  } finally {
    loadingPreview.value = false
  }
}

async function pollProgress(taskId: string) {
  if (pollTimer) clearTimeout(pollTimer)
  try {
    progress.value = unwrap<KnowledgeImageCompressionProgress>(
      await getKnowledgeImageCompressionProgress(props.kbId, taskId),
    )
    if (taskRunning.value) {
      pollTimer = setTimeout(() => pollProgress(taskId), 2000)
    } else {
      await loadPreview()
    }
  } catch (error) {
    console.error('Failed to load image compression progress:', error)
    pollTimer = setTimeout(() => pollProgress(taskId), 5000)
  }
}

async function start() {
  starting.value = true
  try {
    const response = unwrap<{ task_id: string }>(await startKnowledgeImageCompression(props.kbId))
    MessagePlugin.success(t('knowledgeEditor.imageCompression.started'))
    await pollProgress(response.task_id)
  } catch (error) {
    console.error('Failed to start knowledge image compression:', error)
    MessagePlugin.error(t('knowledgeEditor.imageCompression.startFailed'))
  } finally {
    starting.value = false
  }
}

function confirmStart() {
  const dialog = DialogPlugin.confirm({
    header: t('knowledgeEditor.imageCompression.confirmTitle'),
    body: t('knowledgeEditor.imageCompression.confirmBody', {
      count: preview.value?.eligible_images || 0,
      size: formatBytes(preview.value?.total_source_bytes || 0),
    }),
    confirmBtn: t('knowledgeEditor.imageCompression.start'),
    cancelBtn: t('common.cancel'),
    onConfirm: async () => {
      dialog.hide()
      await start()
    },
  })
}

onMounted(loadPreview)
onBeforeUnmount(() => {
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<style scoped lang="less">
.image-compression-settings { width: 100%; }
.section-header { margin-bottom: 20px; }
.section-header h2 { margin: 0 0 6px; font-size: 20px; font-weight: 600; color: var(--td-text-color-primary); }
.section-description, .progress-message { margin: 0; color: var(--td-text-color-secondary); line-height: 1.5; }
.loading-state { padding: 32px 0; color: var(--td-text-color-secondary); }
.summary-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; margin: 20px 0; }
.summary-item { padding: 16px; border: 1px solid var(--td-component-stroke); border-radius: var(--td-radius-medium); background: var(--td-bg-color-container); }
.summary-item dt { font-size: 13px; color: var(--td-text-color-secondary); }
.summary-item dd { margin: 8px 0 0; font-size: 20px; font-weight: 600; color: var(--td-text-color-primary); }
.progress-panel { margin: 20px 0; padding: 16px; border: 1px solid var(--td-component-stroke); border-radius: var(--td-radius-medium); }
.progress-heading { display: flex; justify-content: space-between; margin-bottom: 10px; font-weight: 500; }
.progress-message { margin-top: 10px; font-size: 13px; }
.progress-facts { display: flex; flex-wrap: wrap; gap: 8px 20px; margin-top: 12px; font-size: 13px; color: var(--td-text-color-secondary); }
.actions { display: flex; justify-content: flex-end; gap: 12px; margin-top: 24px; }
@media (max-width: 720px) { .summary-grid { grid-template-columns: 1fr; } }
</style>
