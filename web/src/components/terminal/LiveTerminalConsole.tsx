import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowDownToLine,
  Check,
  Copy,
  Download,
  Lock,
  Search,
  Terminal as TerminalIcon,
  Trash2,
  Unlock,
  WrapText,
  X,
} from 'lucide-react';
import type { ExperimentShard, SessionStreamChunk } from '../../types';
import { useI18n } from '../../i18n';
import { cn } from '../ui';

export interface LiveTerminalConsoleProps {
  chunks: SessionStreamChunk[];
  shards?: ExperimentShard[];
  selectedShardFilter: number | 'all';
  onSelectShardFilter?: (shard: number | 'all') => void;
  titleLabel?: string;
  downloadBaseName?: string;
  isStreaming?: boolean;
  onClear?: () => void;
  minHeightClass?: string;
}

interface AnsiSegment {
  text: string;
  color?: string;
  bold?: boolean;
  dim?: boolean;
  underline?: boolean;
}

/**
 * The console is a dedicated code surface: it stays warm obsidian charcoal in both
 * themes (`--code-bg`), using a warm Stardust ANSI palette.
 */
const ANSI_COLOR_MAP: Record<number, string> = {
  30: '#787872',
  31: '#f06449',
  32: '#22c55e',
  33: '#f59e0b',
  34: '#60a5fa',
  35: '#fb923c',
  36: '#2dd4bf',
  37: '#e7e7e4',
  90: '#787872',
  91: '#fb856e',
  92: '#6ee7b7',
  93: '#fcd34d',
  94: '#93c5fd',
  95: '#fdba74',
  96: '#5eead4',
  97: '#ffffff',
};

const TERM_CHROME_BORDER = 'rgba(255, 255, 255, 0.065)';
const TERM_CHROME_FILL = 'rgba(255, 255, 255, 0.025)';
const TERM_STDERR = '#fb856e';
const TERM_SYS = '#7dd3fc';

/**
 * Lightweight, zero-dependency ANSI SGR escape sequence parser with `\r` progress overwrite support.
 */
function parseAnsiText(raw: string): AnsiSegment[] {
  // Handle carriage-return progress updates (keep final overwritten segment per line)
  const normalized = raw
    .split('\n')
    .map((line) => {
      if (!line.includes('\r')) return line;
      const parts = line.split('\r');
      return parts[parts.length - 1];
    })
    .join('\n');

  const segments: AnsiSegment[] = [];
  // Match CSI SGR sequences: ESC [ <params> m
  // eslint-disable-next-line no-control-regex
  const regex = /\x1b\[([0-9;]*)m/g;
  let lastIndex = 0;
  let color: string | undefined;
  let bold = false;
  let dim = false;
  let underline = false;

  let match: RegExpExecArray | null;
  while ((match = regex.exec(normalized)) !== null) {
    if (match.index > lastIndex) {
      segments.push({
        text: normalized.slice(lastIndex, match.index),
        color,
        bold,
        dim,
        underline,
      });
    }
    const codes = match[1] ? match[1].split(';').map((c) => parseInt(c, 10)) : [0];
    for (const code of codes) {
      if (Number.isNaN(code) || code === 0) {
        color = undefined;
        bold = false;
        dim = false;
        underline = false;
      } else if (code === 1) {
        bold = true;
      } else if (code === 2) {
        dim = true;
      } else if (code === 4) {
        underline = true;
      } else if (code === 22) {
        bold = false;
        dim = false;
      } else if (code === 24) {
        underline = false;
      } else if (code === 39) {
        color = undefined;
      } else if (ANSI_COLOR_MAP[code]) {
        color = ANSI_COLOR_MAP[code];
      }
    }
    lastIndex = regex.lastIndex;
  }

  if (lastIndex < normalized.length) {
    segments.push({
      text: normalized.slice(lastIndex),
      color,
      bold,
      dim,
      underline,
    });
  }

  return segments.length > 0 ? segments : [{ text: normalized }];
}

function stripAnsi(raw: string): string {
  // eslint-disable-next-line no-control-regex
  return raw.replace(/\x1b\[[0-9;]*m/g, '');
}

function renderSearchHighlighted(text: string, query: string): React.ReactNode {
  if (!query.trim()) return text;
  const escaped = query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const parts = text.split(new RegExp(`(${escaped})`, 'gi'));
  return parts.map((part, idx) =>
    part.toLowerCase() === query.toLowerCase() ? (
      <mark
        key={idx}
        className="rounded-[3px] px-0.5"
        style={{ backgroundColor: 'rgba(230, 192, 123, 0.28)', color: '#f5e3bd' }}
      >
        {part}
      </mark>
    ) : (
      part
    ),
  );
}

/** Quiet pill on the dark console chrome (stream + shard filters). */
function TermPill({
  active,
  onClick,
  title,
  children,
}: {
  active: boolean;
  onClick: () => void;
  title?: string;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-pressed={active}
      className={cn(
        'ch-num inline-flex items-center gap-1.5 h-6 px-2 shrink-0 rounded-md text-[11px] cursor-pointer transition-colors',
        active ? 'font-medium' : 'hover:bg-white/[0.05]',
      )}
      style={{
        backgroundColor: active ? 'rgba(255, 255, 255, 0.09)' : undefined,
        color: active ? 'var(--code-fg)' : 'var(--code-muted)',
      }}
    >
      {children}
    </button>
  );
}

/** Compact icon/label button on the dark console chrome. */
function TermButton({
  onClick,
  title,
  active = false,
  children,
}: {
  onClick: () => void;
  title: string;
  active?: boolean;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-label={title}
      className="ch-num inline-flex items-center justify-center gap-1 h-6 min-w-6 px-1.5 shrink-0 rounded-md text-[11px] cursor-pointer transition-colors hover:bg-white/[0.07]"
      style={{ color: active ? 'var(--code-fg)' : 'var(--code-muted)' }}
    >
      {children}
    </button>
  );
}

export function LiveTerminalConsole({
  chunks,
  shards,
  selectedShardFilter,
  onSelectShardFilter,
  titleLabel,
  downloadBaseName = 'colabhive-session',
  isStreaming = true,
  onClear,
  minHeightClass = 'h-[430px]',
}: LiveTerminalConsoleProps) {
  const { locale } = useI18n();
  const isZh = locale === 'zh-CN';

  const [streamFilter, setStreamFilter] = useState<'all' | 'stdout' | 'stderr' | 'colab'>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [autoScroll, setAutoScroll] = useState(true);
  const [wrapLines, setWrapLines] = useState(true);
  const [copied, setCopied] = useState(false);

  const viewportRef = useRef<HTMLDivElement | null>(null);

  // Split multi-line chunks into discrete terminal lines for crisp timestamp + stream gutters
  const renderedLines = useMemo(() => {
    const out: Array<{
      id: string;
      timestamp: string;
      stream: 'stdout' | 'stderr' | 'colab';
      shardIndex?: number;
      rawLine: string;
      plainLine: string;
    }> = [];

    chunks.forEach((chunk, cIdx) => {
      if (selectedShardFilter !== 'all' && chunk.shard_index !== selectedShardFilter) {
        return;
      }
      if (streamFilter !== 'all' && chunk.stream !== streamFilter) {
        return;
      }

      const rawText = chunk.text.endsWith('\n') ? chunk.text.slice(0, -1) : chunk.text;
      const lines = rawText.split('\n');
      const d = new Date(chunk.timestamp);
      const ts = Number.isNaN(d.getTime())
        ? '00:00:00'
        : d.toLocaleTimeString('en-GB', { hour12: false });

      lines.forEach((ln, lIdx) => {
        const plain = stripAnsi(ln);
        if (searchQuery.trim() && !plain.toLowerCase().includes(searchQuery.trim().toLowerCase())) {
          return;
        }
        out.push({
          id: `${cIdx}-${lIdx}`,
          timestamp: ts,
          stream: chunk.stream,
          shardIndex: chunk.shard_index,
          rawLine: ln,
          plainLine: plain,
        });
      });
    });

    return out;
  }, [chunks, selectedShardFilter, streamFilter, searchQuery]);

  useEffect(() => {
    if (!autoScroll || !viewportRef.current) return;
    viewportRef.current.scrollTop = viewportRef.current.scrollHeight;
  }, [renderedLines.length, autoScroll]);

  const handleViewportScroll = () => {
    const el = viewportRef.current;
    if (!el) return;
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    if (distanceFromBottom > 48 && autoScroll) {
      setAutoScroll(false);
    } else if (distanceFromBottom <= 16 && !autoScroll) {
      setAutoScroll(true);
    }
  };

  const jumpToTail = () => {
    setAutoScroll(true);
    if (viewportRef.current) {
      viewportRef.current.scrollTop = viewportRef.current.scrollHeight;
    }
  };

  const serializeLines = () =>
    renderedLines
      .map(
        (l) =>
          `${l.timestamp} ${l.shardIndex !== undefined ? `[s${l.shardIndex}] ` : ''}[${l.stream}] ${l.plainLine}`,
      )
      .join('\n');

  const handleCopyAll = async () => {
    try {
      await navigator.clipboard.writeText(serializeLines());
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1800);
    } catch {
      // Ignore clipboard failure
    }
  };

  const handleDownloadLog = () => {
    const blob = new Blob([serializeLines() + '\n'], { type: 'text/plain;charset=utf-8' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    const shardSuffix = selectedShardFilter === 'all' ? 'all' : `s${selectedShardFilter}`;
    a.download = `${downloadBaseName}-${shardSuffix}.log`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  };

  const streamOptions = [
    {
      id: 'all',
      label: isZh ? '全部输出' : 'All',
      title: isZh ? '显示全部日志' : 'Show every log line',
    },
    {
      id: 'stdout',
      label: isZh ? '正常输出' : 'stdout',
      title: isZh ? '只看代码正常打印的内容 (stdout)' : 'Standard output only',
    },
    {
      id: 'stderr',
      label: isZh ? '报错信息' : 'stderr',
      title: isZh ? '只看报错与警告 (stderr)' : 'Errors and warnings only',
    },
    {
      id: 'colab',
      label: isZh ? '系统提示' : 'sys',
      title: isZh ? '只看 ColabHive 的系统提示（启动、重连、释放等）' : 'ColabHive system notices only',
    },
  ] as const;

  return (
    <div
      className="flex flex-col rounded-2xl overflow-hidden border"
      style={{
        backgroundColor: 'var(--code-bg)',
        borderColor: 'var(--code-border)',
        color: 'var(--code-fg)',
      }}
    >
      {/* Toolbar */}
      <div
        className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5 px-2.5 py-1.5 border-b"
        style={{ borderColor: TERM_CHROME_BORDER, backgroundColor: TERM_CHROME_FILL }}
      >
        <div className="flex flex-wrap items-center gap-1 min-w-0">
          {titleLabel && (
            <span
              className="mr-1.5 max-w-[14rem] truncate text-[11.5px] font-medium"
              style={{ color: 'var(--code-fg)' }}
            >
              {titleLabel}
            </span>
          )}
          {streamOptions.map((opt) => (
            <TermPill
              key={opt.id}
              active={streamFilter === opt.id}
              title={opt.title}
              onClick={() => setStreamFilter(opt.id)}
            >
              {opt.label}
            </TermPill>
          ))}
        </div>

        <div className="flex flex-wrap items-center gap-1">
          <label
            className="flex items-center gap-1.5 h-6 px-2 rounded-md border transition-colors focus-within:border-white/20"
            style={{ borderColor: TERM_CHROME_BORDER, backgroundColor: 'rgba(0, 0, 0, 0.22)' }}
          >
            <Search className="w-3 h-3 shrink-0" style={{ color: 'var(--code-muted)' }} />
            <input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder={isZh ? '搜索日志…' : 'Filter logs…'}
              aria-label={isZh ? '搜索日志' : 'Filter logs'}
              className="ch-num w-28 sm:w-36 bg-transparent border-none outline-none text-[11.5px] placeholder:text-[var(--code-muted)]"
              style={{ color: 'var(--code-fg)' }}
            />
            {searchQuery && (
              <button
                type="button"
                onClick={() => setSearchQuery('')}
                aria-label={isZh ? '清除搜索' : 'Clear filter'}
                className="cursor-pointer opacity-70 hover:opacity-100"
                style={{ color: 'var(--code-muted)' }}
              >
                <X className="w-3 h-3" />
              </button>
            )}
          </label>

          <TermButton
            active={autoScroll}
            title={
              isZh
                ? `自动滚屏：有新日志时自动滚到最底部（当前${autoScroll ? '已开启' : '已关闭'}）`
                : `Auto-scroll to the newest line (${autoScroll ? 'on' : 'off'})`
            }
            onClick={() => {
              const next = !autoScroll;
              setAutoScroll(next);
              if (next && viewportRef.current) {
                viewportRef.current.scrollTop = viewportRef.current.scrollHeight;
              }
            }}
          >
            {autoScroll ? <Lock className="w-3 h-3" /> : <Unlock className="w-3 h-3" />}
            <span className="hidden xl:inline">{isZh ? '自动滚屏' : 'Tail'}</span>
          </TermButton>

          <TermButton
            active={wrapLines}
            title={
              isZh
                ? `自动换行：过长的日志自动折行显示（当前${wrapLines ? '已开启' : '已关闭'}）`
                : `Wrap long lines (${wrapLines ? 'on' : 'off'})`
            }
            onClick={() => setWrapLines((w) => !w)}
          >
            <WrapText className="w-3 h-3" />
            <span className="hidden xl:inline">{isZh ? '自动换行' : 'Wrap'}</span>
          </TermButton>

          <TermButton
            active={copied}
            title={isZh ? '复制当前日志（按当前筛选结果）' : 'Copy the logs shown'}
            onClick={() => void handleCopyAll()}
          >
            {copied ? (
              <Check className="w-3 h-3" style={{ color: 'var(--code-string)' }} />
            ) : (
              <Copy className="w-3 h-3" />
            )}
            <span className="hidden sm:inline">
              {copied ? (isZh ? '已复制' : 'Copied') : isZh ? '复制' : 'Copy'}
            </span>
          </TermButton>

          <TermButton
            title={isZh ? '下载 .log 日志到本地' : 'Download as a .log file'}
            onClick={handleDownloadLog}
          >
            <Download className="w-3 h-3" />
            <span>.log</span>
          </TermButton>

          {onClear && (
            <TermButton
              title={isZh ? '清空屏幕（只清空显示，不影响云端任务）' : 'Clear the screen (the task keeps running)'}
              onClick={onClear}
            >
              <Trash2 className="w-3 h-3" />
            </TermButton>
          )}

          <span
            className="ch-num pl-1.5 text-[11px] whitespace-nowrap"
            style={{ color: 'var(--code-muted)' }}
          >
            {renderedLines.length} {isZh ? '行' : 'lines'}
          </span>
        </div>
      </div>

      {/* Shard filter bar */}
      {shards && shards.length > 0 && onSelectShardFilter && (
        <div
          className="flex items-center gap-1 px-2.5 py-1.5 border-b overflow-x-auto"
          style={{ borderColor: TERM_CHROME_BORDER }}
        >
          <TermPill
            active={selectedShardFilter === 'all'}
            title={isZh ? '同时查看全部并行卡的日志' : 'Show logs from every shard'}
            onClick={() => onSelectShardFilter('all')}
          >
            {isZh ? `全部并行卡 · ${shards.length}` : `All Shards · ${shards.length}`}
          </TermPill>

          {shards.map((sh) => {
            const isDead =
              sh.zombie_detected || sh.status.includes('FAIL') || sh.status.includes('ZOMBIE');
            const isDone = sh.status.includes('COMPLETED');
            const dotColor = isDead
              ? 'var(--coral)'
              : isDone
                ? 'var(--sky)'
                : 'var(--emerald)';
            const stateLabel = isDead
              ? isZh
                ? '异常'
                : 'failed'
              : isDone
                ? isZh
                  ? '已完成'
                  : 'done'
                : isZh
                  ? '运行中'
                  : 'running';

            return (
              <TermPill
                key={sh.shard_id}
                active={selectedShardFilter === sh.shard_id}
                onClick={() => onSelectShardFilter(sh.shard_id)}
                title={
                  isZh
                    ? `只看并行卡 #${sh.shard_id} 的日志 · ${sh.accelerator} · ${stateLabel}`
                    : `Shard #${sh.shard_id} · ${sh.accelerator} · ${stateLabel}`
                }
              >
                <span
                  className="w-[5px] h-[5px] rounded-full shrink-0"
                  style={{ backgroundColor: dotColor }}
                />
                <span>{isZh ? `并行卡 #${sh.shard_id}` : `s${sh.shard_id}`}</span>
                <span style={{ opacity: 0.7 }}>{Math.round(sh.progress_pct)}%</span>
              </TermPill>
            );
          })}
        </div>
      )}

      {/* Output viewport */}
      <div className="relative flex-1 min-h-0">
        <div
          ref={viewportRef}
          onScroll={handleViewportScroll}
          className={cn(
            'overflow-auto px-3 py-2.5 font-mono text-[12px] leading-[1.65] select-text',
            minHeightClass,
          )}
          style={{ fontFamily: 'var(--font-mono)' }}
        >
          {renderedLines.length === 0 ? (
            <div className="h-full flex flex-col items-center justify-center py-14 text-center">
              <TerminalIcon className="w-5 h-5 mb-2.5" style={{ color: 'var(--code-muted)' }} />
              <p
                className="text-[12px]"
                style={{ color: 'var(--code-muted)', fontFamily: 'var(--font-sans)' }}
              >
                {searchQuery
                  ? isZh
                    ? `没有找到包含 “${searchQuery}” 的日志`
                    : `No log lines matching “${searchQuery}”`
                  : isZh
                    ? '正在等待云端输出日志…'
                    : 'Waiting for live output…'}
              </p>
            </div>
          ) : (
            <div>
              {renderedLines.map((line) => {
                const ansiSegments = parseAnsiText(line.rawLine);
                const streamColor =
                  line.stream === 'stderr'
                    ? TERM_STDERR
                    : line.stream === 'colab'
                      ? TERM_SYS
                      : 'var(--code-muted)';
                const baseColor =
                  line.stream === 'stderr'
                    ? TERM_STDERR
                    : line.stream === 'colab'
                      ? '#bae6fd'
                      : 'var(--code-fg)';

                return (
                  <div
                    key={line.id}
                    className={cn(
                      'flex items-start gap-2.5 px-1.5 rounded-[4px] transition-colors hover:bg-white/[0.035]',
                      wrapLines ? 'whitespace-pre-wrap break-words' : 'whitespace-pre',
                    )}
                  >
                    <span
                      className="shrink-0 select-none text-[11px] tabular-nums"
                      style={{ color: 'var(--code-comment)' }}
                    >
                      {line.timestamp}
                    </span>

                    {line.shardIndex !== undefined && (
                      <span
                        className="shrink-0 select-none w-5 text-[11px]"
                        style={{ color: 'var(--code-muted)' }}
                      >
                        s{line.shardIndex}
                      </span>
                    )}

                    <span
                      className="shrink-0 select-none w-10 text-[11px]"
                      style={{ color: streamColor, opacity: line.stream === 'stdout' ? 0.75 : 1 }}
                    >
                      {line.stream === 'colab' ? 'sys' : line.stream === 'stderr' ? 'err' : 'out'}
                    </span>

                    <span className="flex-1 min-w-0">
                      {ansiSegments.map((seg, idx) => (
                        <span
                          key={idx}
                          style={{
                            color: seg.color || baseColor,
                            fontWeight: seg.bold ? 600 : 400,
                            opacity: seg.dim ? 0.65 : 1,
                            textDecoration: seg.underline ? 'underline' : undefined,
                          }}
                        >
                          {renderSearchHighlighted(seg.text, searchQuery)}
                        </span>
                      ))}
                    </span>
                  </div>
                );
              })}
            </div>
          )}
        </div>

        {/* Floating jump-to-tail pill when scrolled up */}
        {!autoScroll && renderedLines.length > 0 && (
          <button
            type="button"
            onClick={jumpToTail}
            className="absolute bottom-3 right-4 inline-flex items-center gap-1.5 h-7 px-3 rounded-full border text-[11.5px] font-medium cursor-pointer transition-colors hover:bg-white/[0.12]"
            style={{
              backgroundColor: 'rgba(26, 26, 24, 0.94)',
              borderColor: 'rgba(255, 255, 255, 0.14)',
              color: 'var(--code-fg)',
              boxShadow: '0 8px 24px -6px rgba(0, 0, 0, 0.6)',
              backdropFilter: 'blur(8px)',
            }}
          >
            <ArrowDownToLine className="w-3.5 h-3.5" />
            <span>{isZh ? '回到最新日志' : 'Jump to Latest'}</span>
          </button>
        )}
      </div>

      {/* Connection footer */}
      <div
        className="flex items-center justify-between gap-3 h-7 px-3 border-t text-[11px]"
        style={{ borderColor: TERM_CHROME_BORDER, color: 'var(--code-muted)' }}
      >
        <span className="inline-flex items-center gap-2 min-w-0">
          <span
            data-tone={isStreaming ? 'emerald' : 'neutral'}
            className={cn('ui-dot', isStreaming && 'ui-dot-pulse')}
            style={isStreaming ? undefined : { backgroundColor: 'var(--code-muted)' }}
          />
          <span className="truncate">
            {isStreaming
              ? isZh
                ? '已连接云端实时日志 · 防掉线保活中'
                : 'Live logs connected · keep-alive on'
              : isZh
                ? '实例已停止 · 正在显示历史日志'
                : 'Session stopped · showing saved output'}
          </span>
        </span>
        {searchQuery.trim() && (
          <span className="ch-num shrink-0">
            {isZh ? `搜索：${searchQuery.trim()}` : `filter: ${searchQuery.trim()}`}
          </span>
        )}
      </div>
    </div>
  );
}
