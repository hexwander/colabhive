import type { RuntimeSession } from '../types';

interface FriendlyModelLabel {
  zh: string;
  en: string;
}

const KNOWN_LLM_SLUG_LABELS: Record<string, FriendlyModelLabel> = {
  'qwen3-5-9b-heretic': {
    zh: 'Qwen3.5 9B 去审查版',
    en: 'Qwen3.5 9B Abliterated',
  },
  'gemma4-12b-heretic': {
    zh: 'Gemma 4 12B Heretic',
    en: 'Gemma 4 12B Heretic',
  },
  'qwen3-8-27b-abliterated': {
    zh: 'Qwen3.8 27B 去审查版',
    en: 'Qwen3.8 27B Abliterated',
  },
  'qwen3-8-27b-heretic-coder': {
    zh: 'Qwen3.8 27B Heretic NEO',
    en: 'Qwen3.8 27B Heretic NEO',
  },
  'gemma4-31b-heretic': {
    zh: 'Gemma 4 31B Heretic',
    en: 'Gemma 4 31B Heretic',
  },
  'glm5-3-flash-uncensored': {
    zh: 'GLM-5.3-Flash 无审查版',
    en: 'GLM-5.3-Flash Uncensored',
  },
  'qwen3-8-flash-next-abliterated': {
    zh: 'Qwen3.8-Flash-Next 去审查',
    en: 'Qwen3.8-Flash-Next Abliterated',
  },
  'deepseek-v4-1-flash-uncensored': {
    zh: 'DeepSeek-V4.1-Flash 552B',
    en: 'DeepSeek-V4.1-Flash 552B',
  },
  'deepseek-r1-14b': {
    zh: 'DeepSeek-R1 14B',
    en: 'DeepSeek-R1 14B',
  },
  'qwen3-8b': {
    zh: 'Qwen3 8B',
    en: 'Qwen3 8B',
  },
};

export function isLLMGatewaySession(sess: Pick<RuntimeSession, 'name'> | null | undefined): boolean {
  const name = (sess?.name || '').trim().toLowerCase();
  return name.startsWith('llmgw-');
}

function prettifyModelSlug(slug: string): string {
  const cleaned = slug
    .replace(/^hf-co-/i, '')
    .replace(/-gguf(?:-q[0-9a-z_]+)?$/i, '')
    .replace(/-/g, ' ')
    .trim();
  if (!cleaned) return 'Cloud LLM';
  return cleaned
    .split(/\s+/)
    .map((part) => {
      if (/^[0-9]+b$/i.test(part)) return part.toUpperCase();
      if (/^(qwen|gemma|llama|deepseek|mistral|glm)/i.test(part)) {
        return part.charAt(0).toUpperCase() + part.slice(1);
      }
      return part;
    })
    .join(' ');
}

export interface FriendlySessionDisplay {
  isLLM: boolean;
  /** Short clean name without prefix, e.g. "Qwen3.5 9B 去审查版" */
  title: string;
  /** Prefixed name for mixed lists, e.g. "大模型 · Qwen3.5 9B 去审查版" */
  prefixedTitle: string;
  /** Badge text for LLM sessions */
  badge: string;
  rawName: string;
}

export function getFriendlySessionDisplay(
  sess: Pick<RuntimeSession, 'id' | 'name' | 'notebook_name'> | null | undefined,
  zh: boolean,
): FriendlySessionDisplay {
  const rawName = (sess?.name || sess?.id || '').trim();
  if (!isLLMGatewaySession(sess)) {
    const title = sess?.notebook_name || rawName || (zh ? '云端 GPU 实例' : 'Colab Runtime');
    return {
      isLLM: false,
      title,
      prefixedTitle: title,
      badge: '',
      rawName,
    };
  }

  const slug = rawName.toLowerCase().replace(/^llmgw-/, '');
  const known = KNOWN_LLM_SLUG_LABELS[slug];
  const modelTitle = known ? (zh ? known.zh : known.en) : prettifyModelSlug(slug);

  return {
    isLLM: true,
    title: modelTitle,
    prefixedTitle: zh ? `大模型 · ${modelTitle}` : `LLM · ${modelTitle}`,
    badge: zh ? 'AI 大模型推理服务 · /v1' : 'LLM Inference · /v1',
    rawName,
  };
}
