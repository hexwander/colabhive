/**
 * Translates raw ColabHive / Google Colab backend error strings into clear,
 * actionable user-facing guidance while preserving raw technical details for
 * optional inspection.
 */

export interface FriendlyAppError {
  title: string;
  summary: string;
  suggestion?: string;
  canQueueRetry: boolean;
  gpu?: string;
  raw: string;
}

const GPU_MATCH_RE = /\b(H100|G4|A100|L4|T4)\b/i;

function extractGpuTier(raw: string): string | undefined {
  const paren = raw.match(/pooled-account\((H100|G4|A100|L4|T4)\)/i);
  if (paren?.[1]) return paren[1].toUpperCase();
  const req = raw.match(/requested accelerator[^a-z0-9]*(H100|G4|A100|L4|T4)/i);
  if (req?.[1]) return req[1].toUpperCase();
  const any = raw.match(GPU_MATCH_RE);
  return any?.[1] ? any[1].toUpperCase() : undefined;
}

function alternativeGpuHint(gpu: string | undefined, zh: boolean): string {
  switch (gpu) {
    case 'H100':
      return zh ? 'G4 (96GB) 或 L4 (24GB)' : 'G4 (96GB) or L4 (24GB)';
    case 'G4':
      return zh ? 'L4 (24GB) 或免费 T4 (16GB)' : 'L4 (24GB) or Free T4 (16GB)';
    case 'A100':
      return zh ? 'G4 (96GB)、L4 (24GB) 或免费 T4' : 'G4 (96GB), L4 (24GB), or Free T4';
    case 'L4':
      return zh ? 'G4 (96GB) 或免费 T4 (16GB)' : 'G4 (96GB) or Free T4 (16GB)';
    default:
      return zh ? 'L4 (24GB) 或免费 T4 (16GB)' : 'L4 (24GB) or Free T4 (16GB)';
  }
}

export function formatFriendlyAppError(rawInput: unknown, zh: boolean): FriendlyAppError {
  const raw =
    rawInput instanceof Error
      ? rawInput.message || String(rawInput)
      : typeof rawInput === 'string'
        ? rawInput
        : String(rawInput ?? '');
  const low = raw.toLowerCase();
  const gpu = extractGpuTier(raw);
  const gpuLabel = gpu || (zh ? '目标' : 'target');

  // 1. User cancelled launch
  if (low.includes('context canceled') || low.includes('launch canceled') || low.includes('已取消')) {
    return {
      title: zh ? '已取消启动' : 'Launch Canceled',
      summary: zh
        ? '本次启动已安全中止，没有占用云端显卡，也没有消耗任何 CU 额度。'
        : 'Startup was canceled cleanly with zero GPU allocation or CU usage.',
      canQueueRetry: false,
      gpu,
      raw,
    };
  }

  // 2. Waitlist timed out after polite retries
  if (low.includes('gpu waitlist timed out')) {
    return {
      title: zh
        ? `候补抢卡超时：Google Colab 仍未放出 ${gpuLabel} 显卡`
        : `GPU Waitlist Timed Out for ${gpuLabel}`,
      summary: zh
        ? '在设定的等待时间内，Google Colab 当前机房持续缺卡，已自动停止排队（0 消耗）。'
        : 'Google Colab remained out of capacity through the configured wait window. Queue stopped cleanly with 0 CU burned.',
      suggestion: zh
        ? `建议改选 ${alternativeGpuHint(gpu, true)} 启动，或稍过几分钟后再次开启候补抢卡。`
        : `Try switching to ${alternativeGpuHint(gpu, false)}, or retry waitlist in a few minutes.`,
      canQueueRetry: true,
      gpu,
      raw,
    };
  }

  // 3. Google Colab capacity shortage / 503 / 412 / MAX_CONCURRENCY / cooldown
  const isNoEligible = low.includes('no eligible pooled account');
  const is503 = low.includes('http 503') || low.includes('503 繁忙') || low.includes('资源紧张');
  const is412 = low.includes('http 412') || low.includes('412');
  const is429 = low.includes('http 429') || low.includes('rate limit') || low.includes('too many requests');
  const isMaxConc = low.includes('max_concurrency');
  const isCooldown = low.includes('cooldown');

  if (isNoEligible || is503 || is412 || is429 || isMaxConc || isCooldown) {
    const reasonsZh: string[] = [];
    const reasonsEn: string[] = [];

    if (is503) {
      reasonsZh.push('Google Colab 机房当前繁忙缺卡（HTTP 503）');
      reasonsEn.push('Google Colab backend is temporarily out of capacity (HTTP 503)');
    } else if (is412) {
      reasonsZh.push('Google Colab 当前机房该档位无库存（HTTP 412）');
      reasonsEn.push('Google Colab has no inventory for this GPU tier right now (HTTP 412)');
    } else if (is429) {
      reasonsZh.push('Google Colab 触发临时请求频率限制（HTTP 429）');
      reasonsEn.push('Google Colab temporarily rate-limited requests (HTTP 429)');
    }
    if (isMaxConc) {
      reasonsZh.push('池内部分账号正在运行其他任务，并发槽已满');
      reasonsEn.push('some accounts are already at max concurrency running other jobs');
    }
    if (isCooldown) {
      reasonsZh.push('部分账号该显卡处于防风控短暂冷却中');
      reasonsEn.push('some accounts are in a brief anti-ban cooldown for this GPU');
    }

    const summary = zh
      ? reasonsZh.length > 0
        ? `原因：${reasonsZh.join('；')}。`
        : `当前算力池里暂时没有可分配的 ${gpuLabel} 显卡。`
      : reasonsEn.length > 0
        ? `Why: ${reasonsEn.join('; ')}.`
        : `No pooled account can currently allocate a ${gpuLabel} GPU.`;

    const suggestion = zh
      ? `推荐操作：① 点击「开启安全候补抢卡」（每 15–45 秒礼貌轮询防风控，抢到自动启动）；② 或换选 ${alternativeGpuHint(gpu, true)} / 先释放一台空闲实例。`
      : `Next steps: 1) Click "Enable Safe GPU Waitlist" (polite 15–45s backoff retries, auto-boots once available); or 2) pick ${alternativeGpuHint(gpu, false)} / stop an idle VM.`;

    return {
      title: zh
        ? `当前抢不到 ${gpuLabel} 显卡（Google Colab 算力紧缺或账号已满）`
        : `No ${gpuLabel} GPU Available Right Now (Colab Capacity / Concurrency Full)`,
      summary,
      suggestion,
      canQueueRetry: true,
      gpu,
      raw,
    };
  }

  // 4. Model weight download failures
  if (low.includes('model download failed') || low.includes('hf_token') || low.includes('unsupported model download')) {
    return {
      title: zh ? '模型权重下载失败' : 'Model Weight Download Failed',
      summary: zh
        ? '云端环境无法下载指定的模型权重文件（可能是直链失效、网络中断，或该仓库需要 Hugging Face 访问令牌）。'
        : 'Could not download the selected model weights (URL may be invalid, gated behind HF_TOKEN, or temporarily unreachable).',
      suggestion: zh
        ? '建议检查直链是否可公开下载、在「密钥与代理设置」填入 HF_TOKEN，或改选「空白极速启动」。'
        : 'Verify the URL, add your HF_TOKEN in Settings if gated, or launch Blank mode.',
      canQueueRetry: false,
      gpu,
      raw,
    };
  }

  // 5. Timeout / network
  if (low.includes('timed out') || low.includes('timeout') || low.includes('deadline exceeded')) {
    return {
      title: zh ? '云端启动响应超时' : 'Cloud Startup Timed Out',
      summary: zh
        ? '云端依赖安装或网络桥接耗时过长导致本次请求超时。'
        : 'Cloud provisioning or tunnel handshake took longer than expected.',
      suggestion: zh
        ? '请检查本机代理连接是否稳定后重试，或在「实验工作台」确认是否有残留实例。'
        : 'Check network/proxy stability and retry.',
      canQueueRetry: false,
      gpu,
      raw,
    };
  }

  // 6. Clean fallback preserving original message
  return {
    title: zh ? '启动未能完成' : 'Launch Could Not Complete',
    summary: raw || (zh ? '发生未知错误，请稍后重试。' : 'An unexpected error occurred.'),
    canQueueRetry: false,
    gpu,
    raw,
  };
}
