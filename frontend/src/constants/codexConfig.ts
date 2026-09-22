import type { GroupPlatform } from '@/types'

// Shared by the generated Codex config and the group editor placeholders.
const defaultModels: Record<GroupPlatform, string> = {
  openai: 'gpt-6-sol',
  anthropic: 'claude-opus-5-5',
  gemini: 'gemini-3.8-flash',
  antigravity: 'claude-opus-5',
  grok: 'grok-4.7',
  kimi: 'kimi-k3',
  zhipu: 'glm-5.3',
  deepseek: 'deepseek-flash',
  minimax: 'MiniMax-M3',
  opencode_go: 'glm-5.3',
  composite: 'gpt-6-sol'
}

export const CODEX_AUTO_REVIEW_MODEL = 'codex-auto-review'

export function getCodexDefaultModel(platform: GroupPlatform): string {
  return defaultModels[platform]
}

export function getCodexReviewModelPlaceholder(
  platform: GroupPlatform,
  configuredDefaultModel: string
): string {
  if (platform === 'openai') return CODEX_AUTO_REVIEW_MODEL
  return configuredDefaultModel.trim() || getCodexDefaultModel(platform)
}
