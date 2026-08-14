/**
 * dsh-trae-solo — TRAE SOLO free model gateway for DeepSeek Harness.
 *
 * Registers the `trae-solo` provider route in `llm-pi-ai` (auto-discovered
 * vision + reasoning models) and bundles the `traework2api` multi-account
 * gateway under `server/` (login, check-in, credit monitoring, per-model
 * concurrency limits, `developer` role normalization for TRAE upstream).
 *
 * The provider's `baseURL` / `apiKeyEnv` / `models` live in
 * `~/.dsh/settings.yaml` under `llm-pi-ai.providers.trae-solo` (deployment
 * specific), NOT hardcoded here — see README for the exact block.
 */
import type { Context } from '@deepseek-ai/cordis'
import Schema from '@deepseek-ai/schemastery'

export const name = 'dsh-trae-solo'

export interface Config {
  /** Warn (instead of failing) when the trae-solo provider is not configured in settings.yaml. */
  warnOnMissingProvider: boolean
}

export const Config: Schema<Config> = Schema.object({
  warnOnMissingProvider: Schema.boolean().default(true),
})

export function apply(ctx: Context, config: Config): void {
  // The provider itself is configured through settings.yaml (llm-pi-ai), not
  // this plugin's config. We only surface a helpful diagnostic when it is
  // absent, so a fresh install knows where to paste the block.
  const llm = ctx.get('llm') as { listProviders?: () => Array<{ id: string }> } | undefined
  const hasTraeSolo = llm?.listProviders?.().some((p) => p.id === 'trae-solo') ?? false
  if (!hasTraeSolo && config.warnOnMissingProvider) {
    ctx.logger.warn(
      'dsh-trae-solo: provider "trae-solo" is not registered. ' +
        'Add the llm-pi-ai.providers.trae-solo block to ~/.dsh/settings.yaml (see README).',
    )
  }
}
