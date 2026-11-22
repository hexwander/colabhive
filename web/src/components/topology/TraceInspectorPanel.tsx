import React, { useMemo, useState } from 'react';
import { AlertTriangle, GitBranch, Pause, Play, ScrollText, Search, X } from 'lucide-react';
import type { ToolTraceEvent } from '../../types';
import { formatCU } from '../../lib/api';
import { useI18n } from '../../i18n';
import {
  ActionButton,
  EmptyState,
  InsetWell,
  LayeredCard,
  SegmentedTabs,
  SlideOver,
  StatusPill,
  type SemanticTone,
} from '../ui';
import { SyntaxCodeBlock } from '../connect/AgentConnectDrawer';
import { formatDurationMs } from '../terminal/workbenchMockData';

export interface TraceInspectorPanelProps {
  traces: ToolTraceEvent[];
  paused: boolean;
  onTogglePaused: () => void;
  callerFilter?: string | null;
  onClearCallerFilter?: () => void;
  /** Position in the page entrance cascade (forwarded to `LayeredCard`). */
  staggerIndex?: number;
}

type StatusFilter = 'all' | 'ok' | 'fallback' | 'error';

function formatPreciseTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  const ss = String(d.getSeconds()).padStart(2, '0');
  const ms = String(d.getMilliseconds()).padStart(3, '0');
  return `${hh}:${mm}:${ss}.${ms}`;
}

/**
 * Pretty-prints a captured payload when it is valid JSON; otherwise returns it
 * untouched. Nothing is synthesised: a missing body renders as a plain note.
 */
function prettyPayload(raw: string | undefined, fallback: string): string {
  if (!raw || !raw.trim()) return fallback;
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

/** Status pill copy: plain words in zh-CN, the familiar HTTP shorthand in en-US. */
function traceStatus(
  tr: ToolTraceEvent,
  isZh: boolean,
): { tone: SemanticTone; label: string; title: string } {
  if (tr.status === 'ok') {
    const code = tr.status_code || 200;
    return {
      tone: 'emerald',
      label: isZh ? '正常' : `${code} OK`,
      title: isZh ? `调用成功（状态码 ${code}）` : `Succeeded (status ${code})`,
    };
  }
  if (tr.status === 'fallback') {
    return {
      tone: 'amber',
      label: isZh ? '已自动换卡' : 'Fallback',
      title: isZh
        ? '目标显卡缺货，已自动换用下一档可用显卡并成功执行'
        : 'The requested GPU was out of stock; the next available GPU was used',
    };
  }
  const code = tr.status_code || 500;
  return {
    tone: 'coral',
    label: isZh ? '失败' : `${code} Error`,
    title: isZh ? `调用失败（状态码 ${code}）` : `Failed (status ${code})`,
  };
}

function MetaItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11.5px] font-medium text-fg-muted">{label}</dt>
      <dd className="ch-num mt-0.5 truncate text-[12.5px] text-fg">{children}</dd>
    </div>
  );
}

export function TraceInspectorPanel({
  traces,
  paused,
  onTogglePaused,
  callerFilter,
  onClearCallerFilter,
  staggerIndex,
}: TraceInspectorPanelProps) {
  const { locale, displayEmail, displaySecret } = useI18n();
  const isZh = locale === 'zh-CN';

  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all');
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedTrace, setSelectedTrace] = useState<ToolTraceEvent | null>(null);

  const filteredTraces = useMemo(() => {
    return traces.filter((tr) => {
      if (callerFilter && tr.caller !== callerFilter) return false;
      if (statusFilter !== 'all' && tr.status !== statusFilter) return false;
      if (searchQuery.trim()) {
        const q = searchQuery.trim().toLowerCase();
        const hay = `${tr.caller} ${tr.tool_or_endpoint} ${tr.transport} ${tr.account_email || ''} ${tr.assigned_gpu || ''} ${tr.fallback_trace || ''}`.toLowerCase();
        if (!hay.includes(q)) return false;
      }
      return true;
    });
  }, [traces, callerFilter, statusFilter, searchQuery]);

  const counts = useMemo(() => {
    let ok = 0;
    let fallback = 0;
    let error = 0;
    traces.forEach((tr) => {
      if (tr.status === 'ok') ok += 1;
      else if (tr.status === 'fallback') fallback += 1;
      else error += 1;
    });
    return { total: traces.length, ok, fallback, error };
  }, [traces]);

  const selectedStatus = selectedTrace ? traceStatus(selectedTrace, isZh) : null;

  return (
    <>
      <LayeredCard padding="none" staggerIndex={staggerIndex}>
        {/* Toolbar */}
        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2.5 px-4 sm:px-5 py-3 border-b border-line-faint">
          <div className="flex flex-wrap items-center gap-2 min-w-0">
            <SegmentedTabs<StatusFilter>
              size="sm"
              aria-label={isZh ? '按状态筛选' : 'Filter by status'}
              value={statusFilter}
              onChange={setStatusFilter}
              options={[
                { value: 'all', label: isZh ? '全部' : 'All', count: counts.total },
                { value: 'ok', label: isZh ? '成功' : 'OK', count: counts.ok },
                {
                  value: 'fallback',
                  label: isZh ? '自动换卡' : 'Fallback',
                  count: counts.fallback,
                },
                { value: 'error', label: isZh ? '失败' : 'Error', count: counts.error },
              ]}
            />

            {callerFilter && (
              <span className="inline-flex items-center gap-1 h-6 pl-2.5 pr-1 rounded-full border border-transparent bg-[var(--accent)] text-[var(--accent-fg)] text-[11px] font-semibold">
                <span className="max-w-[12rem] truncate">
                  {isZh ? '只看：' : 'Caller: '}
                  {callerFilter}
                </span>
                {onClearCallerFilter && (
                  <button
                    type="button"
                    onClick={onClearCallerFilter}
                    aria-label={isZh ? '清除 AI 工具筛选' : 'Clear caller filter'}
                    className="inline-flex items-center justify-center w-4 h-4 rounded-full cursor-pointer transition-opacity hover:opacity-75"
                  >
                    <X className="w-3 h-3" />
                  </button>
                )}
              </span>
            )}
          </div>

          <div className="flex items-center gap-2">
            <div className="relative">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-fg-muted" />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder={isZh ? '搜索操作 / 账号…' : 'Search tool / account…'}
                aria-label={isZh ? '搜索 AI 调用记录' : 'Search the AI call log'}
                className="ui-input h-7 w-40 sm:w-52 pl-8 text-[12px]"
              />
            </div>

            <ActionButton
              size="sm"
              variant="secondary"
              icon={paused ? <Play /> : <Pause />}
              onClick={onTogglePaused}
              title={
                paused
                  ? isZh
                    ? '恢复实时刷新调用记录'
                    : 'Resume live stream'
                  : isZh
                    ? '暂停实时刷新，方便查看当前记录'
                    : 'Pause live stream'
              }
            >
              {paused ? (isZh ? '恢复' : 'Resume') : isZh ? '暂停' : 'Pause'}
            </ActionButton>
          </div>
        </div>

        {/* Trace table */}
        {filteredTraces.length === 0 ? (
          <EmptyState
            icon={<ScrollText />}
            title={
              traces.length === 0
                ? isZh
                  ? '还没有 AI 调用记录'
                  : 'No AI calls yet'
                : isZh
                  ? '没有匹配的调用记录'
                  : 'No matching calls'
            }
            description={
              traces.length === 0
                ? isZh
                  ? '接入的 AI 工具开始调用云端显卡后，每一次操作都会实时出现在这里。'
                  : 'Every call appears here in real time once a connected AI tool starts using cloud GPUs.'
                : isZh
                  ? '调整状态筛选或搜索关键词后重试。'
                  : 'Try a different status filter or search term.'
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-left text-[12.5px]">
              <thead>
                <tr className="border-b border-line-faint text-[11.5px] font-medium text-fg-muted">
                  <th className="py-2.5 pl-4 sm:pl-5 pr-3 font-medium">{isZh ? '时间' : 'Time'}</th>
                  <th className="py-2.5 px-3 font-medium">{isZh ? 'AI 工具' : 'Caller'}</th>
                  <th className="py-2.5 px-3 font-medium">
                    {isZh ? '执行的操作' : 'Tool / Endpoint'}
                  </th>
                  <th className="py-2.5 px-3 font-medium">{isZh ? '账号' : 'Account'}</th>
                  <th className="py-2.5 px-3 font-medium">{isZh ? '显卡' : 'GPU'}</th>
                  <th className="py-2.5 px-3 font-medium text-right">{isZh ? '耗时' : 'Latency'}</th>
                  <th className="py-2.5 px-3 font-medium">{isZh ? '状态' : 'Status'}</th>
                  <th className="py-2.5 pl-3 pr-4 sm:pr-5 font-medium text-right">
                    {isZh ? '消耗额度' : 'CU'}
                  </th>
                </tr>
              </thead>

              <tbody className="divide-y divide-line-faint">
                {filteredTraces.map((tr) => {
                  const status = traceStatus(tr, isZh);
                  const fellBack =
                    Boolean(tr.requested_gpu && tr.assigned_gpu) &&
                    tr.requested_gpu !== tr.assigned_gpu;
                  const isSelected = selectedTrace?.id === tr.id;

                  return (
                    <tr
                      key={tr.id}
                      tabIndex={0}
                      onClick={() => setSelectedTrace(tr)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault();
                          setSelectedTrace(tr);
                        }
                      }}
                      className={
                        isSelected
                          ? 'cursor-pointer bg-accent-soft outline-none'
                          : 'cursor-pointer transition-colors hover:bg-[var(--hover-fill)] focus-visible:bg-[var(--hover-fill)] outline-none'
                      }
                    >
                      <td className="py-2.5 pl-4 sm:pl-5 pr-3 ch-num text-[11.5px] text-fg-muted whitespace-nowrap">
                        {formatPreciseTime(tr.timestamp)}
                      </td>

                      <td className="py-2.5 px-3 whitespace-nowrap">
                        <div className="font-medium text-fg">{tr.caller}</div>
                        <div className="ch-num text-[11px] text-fg-muted">
                          {tr.transport}
                          {tr.api_key_prefix ? ` · ${displaySecret(tr.api_key_prefix)}` : ''}
                        </div>
                      </td>

                      <td className="py-2.5 px-3 font-mono ch-num text-[12px] text-fg whitespace-nowrap">
                        {tr.tool_or_endpoint}
                      </td>

                      <td className="py-2.5 px-3 ch-num text-[12px] text-fg-secondary whitespace-nowrap">
                        {tr.account_email ? displayEmail(tr.account_email) : '—'}
                      </td>

                      <td className="py-2.5 px-3 whitespace-nowrap">
                        {fellBack ? (
                          <StatusPill
                            size="xs"
                            mono
                            tone="amber"
                            title={
                              isZh
                                ? `${tr.requested_gpu} 缺货，已自动换用 ${tr.assigned_gpu}`
                                : `${tr.requested_gpu} was out of stock; switched to ${tr.assigned_gpu}`
                            }
                          >
                            {tr.requested_gpu} → {tr.assigned_gpu}
                          </StatusPill>
                        ) : tr.assigned_gpu || tr.requested_gpu ? (
                          <StatusPill size="xs" mono>
                            {tr.assigned_gpu || tr.requested_gpu}
                          </StatusPill>
                        ) : (
                          <span className="text-fg-muted">—</span>
                        )}
                      </td>

                      <td className="py-2.5 px-3 text-right ch-num text-fg-secondary whitespace-nowrap">
                        {formatDurationMs(tr.duration_ms)}
                      </td>

                      <td className="py-2.5 px-3 whitespace-nowrap">
                        <StatusPill
                          size="xs"
                          mono={!isZh}
                          tone={status.tone}
                          dot
                          title={status.title}
                        >
                          {status.label}
                        </StatusPill>
                      </td>

                      <td className="py-2.5 pl-3 pr-4 sm:pr-5 text-right ch-num whitespace-nowrap">
                        {tr.cu_delta > 0 ? (
                          <span className="text-fg">+{formatCU(tr.cu_delta, 4)}</span>
                        ) : (
                          <span className="text-fg-muted">—</span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </LayeredCard>

      {/* Trace detail drawer */}
      <SlideOver
        open={selectedTrace !== null}
        onClose={() => setSelectedTrace(null)}
        width="xl"
        icon={<ScrollText />}
        title={<span className="ch-num">{selectedTrace?.tool_or_endpoint ?? ''}</span>}
        subtitle={
          selectedTrace && selectedStatus ? (
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span>{selectedTrace.caller}</span>
              <span aria-hidden="true" className="text-fg-muted">
                ·
              </span>
              <span className="ch-num">{selectedTrace.transport}</span>
              <span aria-hidden="true" className="text-fg-muted">
                ·
              </span>
              <span className="ch-num">{formatPreciseTime(selectedTrace.timestamp)}</span>
              <StatusPill
                size="xs"
                mono={!isZh}
                tone={selectedStatus.tone}
                dot
                title={selectedStatus.title}
              >
                {selectedStatus.label}
              </StatusPill>
            </span>
          ) : undefined
        }
      >
        {selectedTrace && (
          <div className="space-y-4">
            <InsetWell padding="md">
              <dl className="grid grid-cols-2 gap-x-5 gap-y-3.5">
                <MetaItem label={isZh ? 'AI 工具' : 'Caller'}>{selectedTrace.caller}</MetaItem>
                <MetaItem label={isZh ? '访问密钥' : 'Key Prefix'}>
                  {selectedTrace.api_key_prefix
                    ? displaySecret(selectedTrace.api_key_prefix)
                    : '—'}
                </MetaItem>
                <MetaItem label={isZh ? '账号' : 'Account'}>
                  {selectedTrace.account_email ? displayEmail(selectedTrace.account_email) : '—'}
                </MetaItem>
                <MetaItem label={isZh ? '云端实例' : 'Session ID'}>
                  {selectedTrace.session_id ? displaySecret(selectedTrace.session_id) : '—'}
                </MetaItem>
                <MetaItem label={isZh ? '使用显卡' : 'GPU Tier'}>
                  {selectedTrace.requested_gpu || selectedTrace.assigned_gpu
                    ? selectedTrace.requested_gpu &&
                      selectedTrace.assigned_gpu &&
                      selectedTrace.requested_gpu !== selectedTrace.assigned_gpu
                      ? `${selectedTrace.requested_gpu} → ${selectedTrace.assigned_gpu}`
                      : selectedTrace.assigned_gpu || selectedTrace.requested_gpu
                    : '—'}
                </MetaItem>
                <MetaItem label={isZh ? '耗时' : 'Duration'}>
                  {formatDurationMs(selectedTrace.duration_ms)}
                </MetaItem>
                <MetaItem label={isZh ? '状态码' : 'HTTP / RPC Status'}>
                  {selectedTrace.status_code || '—'}
                </MetaItem>
                <MetaItem label={isZh ? '消耗额度' : 'CU Delta'}>
                  {selectedTrace.cu_delta > 0 ? `+${formatCU(selectedTrace.cu_delta, 4)}` : '—'}
                </MetaItem>
              </dl>
            </InsetWell>

            {selectedTrace.fallback_trace && (
              <div
                data-tone="amber"
                className="flex items-start gap-2.5 rounded-xl border px-3.5 py-3"
                style={{ backgroundColor: 'var(--tone-soft)', borderColor: 'var(--tone-border)' }}
              >
                <GitBranch
                  className="w-4 h-4 shrink-0 mt-0.5"
                  style={{ color: 'var(--tone-fg)' }}
                />
                <div className="min-w-0">
                  <div className="text-[12.5px] font-semibold" style={{ color: 'var(--tone-fg)' }}>
                    {isZh ? '自动换卡详情' : 'Automatic GPU switch details'}
                  </div>
                  <div className="ch-num mt-1 text-[12px] leading-relaxed text-fg-secondary break-words">
                    {selectedTrace.fallback_trace}
                  </div>
                </div>
              </div>
            )}

            {selectedTrace.error_message && (
              <div
                data-tone="coral"
                className="flex items-start gap-2.5 rounded-xl border px-3.5 py-3"
                style={{ backgroundColor: 'var(--tone-soft)', borderColor: 'var(--tone-border)' }}
              >
                <AlertTriangle
                  className="w-4 h-4 shrink-0 mt-0.5"
                  style={{ color: 'var(--tone-fg)' }}
                />
                <div className="min-w-0">
                  <div className="text-[12.5px] font-semibold" style={{ color: 'var(--tone-fg)' }}>
                    {isZh ? '调用失败原因' : 'Request failed'}
                  </div>
                  <div className="ch-num mt-1 text-[12px] leading-relaxed text-fg-secondary break-words">
                    {selectedTrace.error_message}
                  </div>
                </div>
              </div>
            )}

            <SyntaxCodeBlock
              language="json"
              label={isZh ? '请求参数' : 'Request Payload'}
              copyLabel={isZh ? '复制' : 'Copy'}
              copiedLabel={isZh ? '已复制' : 'Copied'}
              code={prettyPayload(
                selectedTrace.request_preview,
                isZh ? '// 本次调用没有记录请求参数' : '// No request body captured',
              )}
            />

            <SyntaxCodeBlock
              language="json"
              label={isZh ? '返回结果' : 'Response Payload'}
              copyLabel={isZh ? '复制' : 'Copy'}
              copiedLabel={isZh ? '已复制' : 'Copied'}
              code={prettyPayload(
                selectedTrace.response_preview,
                selectedTrace.error_message ||
                  (isZh ? '// 本次调用没有记录返回结果' : '// No response body captured'),
              )}
            />
          </div>
        )}
      </SlideOver>
    </>
  );
}
