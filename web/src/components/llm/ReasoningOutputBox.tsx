import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Brain, ChevronDown, ChevronRight, Loader2, Sparkles } from 'lucide-react';
import { cn } from '../ui/primitives';

export interface ParsedReasoning {
  thinking: string;
  answer: string;
  hasThinking: boolean;
  isThinkingOpen: boolean;
}

const OPEN_TAGS = ['<think>', '<thought>', '<reasoning>'] as const;
const CLOSE_TAGS = ['</think>', '</thought>', '</reasoning>'] as const;

/**
 * Splits a streaming or completed LLM reply into structured Chain-of-Thought (`thinking`)
 * and final `answer`, supporting both structured `reasoning_content` / `thinking_delta`
 * channels and inline `<think>...</think>` / `<thought>...</thought>` / `<reasoning>...</reasoning>` tags.
 */
export function parseReasoningSegments(
  rawText: string,
  separateThinking?: string,
): ParsedReasoning {
  const thinkParts: string[] = [];
  if (separateThinking && separateThinking.trim()) {
    thinkParts.push(separateThinking.trim());
  }

  let cursor = rawText || '';
  const answerParts: string[] = [];
  let isThinkingOpen = false;

  while (cursor.length > 0) {
    let earliestIdx = -1;
    let matchedIdx = -1;
    const lower = cursor.toLowerCase();
    for (let i = 0; i < OPEN_TAGS.length; i++) {
      const idx = lower.indexOf(OPEN_TAGS[i]);
      if (idx >= 0 && (earliestIdx === -1 || idx < earliestIdx)) {
        earliestIdx = idx;
        matchedIdx = i;
      }
    }

    if (earliestIdx === -1) {
      // Handle stray closing tag if an upstream stream omitted the opening tag
      let strayCloseIdx = -1;
      let strayCloseLen = 0;
      for (const ct of CLOSE_TAGS) {
        const ci = lower.indexOf(ct);
        if (ci >= 0 && (strayCloseIdx === -1 || ci < strayCloseIdx)) {
          strayCloseIdx = ci;
          strayCloseLen = ct.length;
        }
      }
      if (strayCloseIdx >= 0) {
        const beforeClose = cursor.slice(0, strayCloseIdx).trim();
        if (beforeClose) thinkParts.push(beforeClose);
        cursor = cursor.slice(strayCloseIdx + strayCloseLen).replace(/^\s+/, '');
        continue;
      }
      answerParts.push(cursor);
      break;
    }

    if (earliestIdx > 0) {
      answerParts.push(cursor.slice(0, earliestIdx));
    }

    const openTag = OPEN_TAGS[matchedIdx];
    const closeTag = CLOSE_TAGS[matchedIdx];
    const afterOpen = cursor.slice(earliestIdx + openTag.length);
    const closeIdx = afterOpen.toLowerCase().indexOf(closeTag);

    if (closeIdx === -1) {
      // Still actively streaming inside `<think>...`
      isThinkingOpen = true;
      const partialThink = afterOpen.replace(/^\r?\n/, '');
      if (partialThink) thinkParts.push(partialThink);
      break;
    }

    const closedThink = afterOpen.slice(0, closeIdx).trim();
    if (closedThink) thinkParts.push(closedThink);
    cursor = afterOpen.slice(closeIdx + closeTag.length).replace(/^\r?\n+/, '');
  }

  const thinking = thinkParts.join('\n\n');
  const answer = answerParts.join('').trimStart();
  const hasThinking = thinking.length > 0 || isThinkingOpen;

  return { thinking, answer, hasThinking, isThinkingOpen };
}

/**
 * Lightweight, zero-dependency, XSS-safe inline Markdown renderer for bold + inline code.
 */
function renderInlineFormatting(line: string, keyPrefix: string): React.ReactNode[] {
  const nodes: React.ReactNode[] = [];
  const tokenRe = /(`[^`]+`|\*\*[^*]+\*\*)/g;
  let lastIdx = 0;
  let match: RegExpExecArray | null;
  let partIdx = 0;
  while ((match = tokenRe.exec(line)) !== null) {
    if (match.index > lastIdx) {
      nodes.push(line.slice(lastIdx, match.index));
    }
    const tok = match[0];
    if (tok.startsWith('`') && tok.endsWith('`')) {
      nodes.push(
        <code
          key={`${keyPrefix}-c-${partIdx++}`}
          className="px-1.5 py-0.5 rounded-md text-[11.5px] ch-num bg-surface-1 border"
          style={{ borderColor: 'var(--border-faint)' }}
        >
          {tok.slice(1, -1)}
        </code>,
      );
    } else if (tok.startsWith('**') && tok.endsWith('**')) {
      nodes.push(
        <strong key={`${keyPrefix}-b-${partIdx++}`} className="font-semibold text-fg">
          {tok.slice(2, -2)}
        </strong>,
      );
    }
    lastIdx = match.index + tok.length;
  }
  if (lastIdx < line.length) {
    nodes.push(line.slice(lastIdx));
  }
  return nodes;
}

function LightweightMarkdown({ text }: { text: string }) {
  const segments = useMemo(() => {
    const out: Array<{ kind: 'text' | 'code'; lang?: string; body: string }> = [];
    const fenceRe = /```([a-zA-Z0-9_-]*)\r?\n([\s\S]*?)(?:```|$)/g;
    let last = 0;
    let m: RegExpExecArray | null;
    while ((m = fenceRe.exec(text)) !== null) {
      if (m.index > last) {
        out.push({ kind: 'text', body: text.slice(last, m.index) });
      }
      out.push({ kind: 'code', lang: m[1] || undefined, body: m[2] });
      last = m.index + m[0].length;
    }
    if (last < text.length) {
      out.push({ kind: 'text', body: text.slice(last) });
    }
    return out;
  }, [text]);

  return (
    <div className="space-y-2 text-[12.5px] leading-relaxed text-fg break-words">
      {segments.map((seg, si) => {
        if (seg.kind === 'code') {
          return (
            <pre
              key={`code-${si}`}
              className="rounded-lg border bg-surface-1 px-3 py-2 text-[11.5px] ch-num overflow-x-auto leading-relaxed"
              style={{ borderColor: 'var(--border-faint)' }}
            >
              <code>{seg.body}</code>
            </pre>
          );
        }
        const lines = seg.body.split('\n');
        return (
          <div key={`txt-${si}`} className="space-y-1 whitespace-pre-wrap">
            {lines.map((line, li) => {
              const trimmed = line.trim();
              if (!trimmed) return <div key={`sp-${li}`} className="h-1" />;
              if (/^[-*]\s+/.test(trimmed)) {
                return (
                  <div key={`li-${li}`} className="flex items-start gap-2 pl-1">
                    <span className="text-fg-muted select-none mt-0.5">•</span>
                    <span className="min-w-0 flex-1">
                      {renderInlineFormatting(trimmed.replace(/^[-*]\s+/, ''), `li-${si}-${li}`)}
                    </span>
                  </div>
                );
              }
              return (
                <div key={`ln-${li}`}>
                  {renderInlineFormatting(line, `ln-${si}-${li}`)}
                </div>
              );
            })}
          </div>
        );
      })}
    </div>
  );
}

export interface ReasoningOutputBoxProps {
  /** Raw answer or mixed `<think>...</think>` reply stream */
  rawText?: string;
  /** Optional dedicated reasoning channel (`reasoning_content` / `thinking_delta`) */
  thinkingText?: string;
  /** True while tokens are actively streaming */
  streaming: boolean;
  /** Elapsed thinking time in milliseconds (optional) */
  thinkingDurationMs?: number;
  /** True if the user manually stopped generation mid-stream */
  aborted?: boolean;
  zh: boolean;
  waitingText?: string;
}

export function ReasoningOutputBox({
  rawText = '',
  thinkingText = '',
  streaming,
  thinkingDurationMs,
  aborted = false,
  zh,
  waitingText,
}: ReasoningOutputBoxProps) {
  const parsed = useMemo(
    () => parseReasoningSegments(rawText, thinkingText),
    [rawText, thinkingText],
  );

  const isActivelyThinking =
    streaming &&
    parsed.hasThinking &&
    parsed.answer.length === 0 &&
    (parsed.isThinkingOpen || thinkingText.length > 0);

  // Follow Cherry Studio / Open WebUI / Claude UX:
  // auto-expand while actively thinking, auto-collapse the instant final answer begins (unless user manually toggled).
  const [userToggled, setUserToggled] = useState<boolean | null>(null);
  const prevHasAnswerRef = useRef(false);
  const thinkScrollRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!parsed.hasThinking && !streaming) {
      setUserToggled(null);
      prevHasAnswerRef.current = false;
    }
  }, [parsed.hasThinking, streaming]);

  useEffect(() => {
    if (parsed.answer.length > 0 && !prevHasAnswerRef.current) {
      prevHasAnswerRef.current = true;
      if (userToggled === null) {
        // Auto-collapse smoothly once final answer starts streaming
        setUserToggled(false);
      }
    }
  }, [parsed.answer.length, userToggled]);

  useEffect(() => {
    if (isActivelyThinking && thinkScrollRef.current) {
      thinkScrollRef.current.scrollTop = thinkScrollRef.current.scrollHeight;
    }
  }, [parsed.thinking, isActivelyThinking]);

  const isExpanded = userToggled !== null ? userToggled : isActivelyThinking;

  const formattedThinkSec =
    typeof thinkingDurationMs === 'number' && thinkingDurationMs >= 100
      ? `${(thinkingDurationMs / 1000).toFixed(1)}s`
      : null;

  if (!parsed.hasThinking && !parsed.answer) {
    return (
      <div className="flex items-center gap-2 text-[12px] text-fg-muted py-0.5">
        {streaming && <Loader2 className="w-3.5 h-3.5 animate-spin shrink-0" />}
        <span>
          {waitingText ||
            (zh
              ? '正在等待云端模型返回首个 Token…'
              : 'Waiting for first token from cloud GPU…')}
        </span>
      </div>
    );
  }

  return (
    <div className="space-y-2.5" data-testid="reasoning-output-box">
      {parsed.hasThinking && (
        <div
          className="rounded-xl border overflow-hidden transition-colors"
          style={
            isActivelyThinking
              ? {
                  backgroundColor: 'var(--amber-soft)',
                  borderColor: 'var(--amber-border)',
                }
              : {
                  backgroundColor: 'var(--surface-1)',
                  borderColor: 'var(--border-faint)',
                }
          }
          data-testid="reasoning-accordion"
        >
          <button
            type="button"
            aria-expanded={isExpanded}
            onClick={() => setUserToggled(!isExpanded)}
            className="w-full px-3 py-2 flex items-center justify-between gap-2 text-left cursor-pointer hover:bg-[var(--hover-fill)] transition-colors"
          >
            <div className="flex items-center gap-2 min-w-0">
              {isActivelyThinking ? (
                <Sparkles
                  className="w-3.5 h-3.5 shrink-0 animate-pulse"
                  style={{ color: 'var(--amber-text)' }}
                />
              ) : (
                <Brain className="w-3.5 h-3.5 shrink-0 text-fg-muted" />
              )}
              <span
                className={cn(
                  'text-[11.5px] font-semibold truncate',
                  isActivelyThinking ? '' : 'text-fg-secondary',
                )}
                style={isActivelyThinking ? { color: 'var(--amber-text)' } : undefined}
              >
                {isActivelyThinking
                  ? zh
                    ? '正在深度思考…'
                    : 'Thinking…'
                  : zh
                    ? '深度思考过程'
                    : 'Thought process'}
              </span>
              <span className="text-[11px] ch-num text-fg-muted shrink-0">
                {formattedThinkSec ? `· ${formattedThinkSec}` : ''}
                {parsed.thinking.length > 0
                  ? zh
                    ? ` · ${parsed.thinking.length} 字`
                    : ` · ${parsed.thinking.length} chars`
                  : ''}
              </span>
            </div>
            <div className="flex items-center gap-1 text-[11px] text-fg-muted shrink-0">
              <span>{isExpanded ? (zh ? '收起' : 'Hide') : zh ? '展开' : 'Show'}</span>
              {isExpanded ? (
                <ChevronDown className="w-3.5 h-3.5" />
              ) : (
                <ChevronRight className="w-3.5 h-3.5" />
              )}
            </div>
          </button>

          {isExpanded && (
            <div
              ref={thinkScrollRef}
              className="px-3 pb-2.5 pt-1 max-h-[150px] overflow-y-auto border-t"
              style={{ borderColor: 'var(--border-faint)' }}
            >
              <div
                className="pl-2.5 border-l-2 text-[11.5px] leading-relaxed text-fg-secondary whitespace-pre-wrap break-words"
                style={{
                  borderColor: isActivelyThinking
                    ? 'var(--amber-border)'
                    : 'var(--border-subtle)',
                }}
              >
                {parsed.thinking || (zh ? '正在组织推理步骤…' : 'Structuring reasoning steps…')}
              </div>
            </div>
          )}
        </div>
      )}

      {parsed.answer ? (
        <div className="relative">
          <LightweightMarkdown text={parsed.answer} />
          {streaming && !isActivelyThinking && (
            <span
              className="inline-block w-1.5 h-3.5 ml-0.5 align-middle rounded-[2px] animate-pulse"
              style={{ backgroundColor: 'var(--accent)' }}
            />
          )}
        </div>
      ) : aborted && parsed.hasThinking ? (
        <div className="text-[11.5px] text-fg-muted italic">
          {zh
            ? '已在思考阶段手动停止输出（未进入正文回答）。'
            : 'Generation stopped manually during reasoning phase.'}
        </div>
      ) : null}
    </div>
  );
}
