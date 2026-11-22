import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Cloud,
  Code2,
  Copy,
  ExternalLink,
  FolderOpen,
  Globe,
  Info,
  KeyRound,
  Layers,
  Plus,
  RefreshCw,
  RotateCcw,
  ShieldCheck,
  SlidersHorizontal,
  Terminal,
  Zap,
} from 'lucide-react';
import type {
  AgentAPIKey,
  AgentClientConfigStatus,
  AgentClientID,
} from '../../types';
import { api } from '../../lib/api';
import { useI18n } from '../../i18n';
import {
  buildFallbackClientConfigs,
  DEMO_API_KEYS,
  sanitizeAgentApiKey,
} from '../layout/telemetryUi';
import {
  ActionButton,
  cn,
  IconButton,
  InsetWell,
  SegmentedTabs,
  SlideOver,
  StatusPill,
  Switch,
} from '../ui/primitives';

type ConnectTargetTab = AgentClientID | 'colab-cli' | 'python-sdk';
type Transport = 'http' | 'stdio';

interface AgentConnectDrawerProps {
  open: boolean;
  onClose: () => void;
  initialKeyPrefix?: string;
  deploymentMode?: 'local' | 'cloud';
  publicUrl?: string;
}

function buildCloudRemoteSnippet(
  clientId: AgentClientID,
  rawBaseUrl: string,
  bearerKey: string,
): string {
  const cleanBase = rawBaseUrl.replace(/\/$/, '');
  const mcpUrl = `${cleanBase}/mcp`;
  const authHeader = `Bearer ${bearerKey}`;

  switch (clientId) {
    case 'codex':
      return `[mcp_servers.colabhive]\nurl = "${mcpUrl}"\ntool_timeout_sec = 1800\nhttp_headers = { Authorization = "${authHeader}" }`;
    case 'opencode':
      return `{\n  // ColabHive remote multi-account GPU pool\n  "mcp": {\n    "colabhive": {\n      "type": "remote",\n      "url": "${mcpUrl}",\n      "headers": {\n        "Authorization": "${authHeader}"\n      },\n      "enabled": true\n    }\n  }\n}`;
    case 'claude-code':
      return JSON.stringify(
        {
          mcpServers: {
            colabhive: {
              type: 'http',
              url: mcpUrl,
              headers: { Authorization: authHeader },
            },
          },
        },
        null,
        2,
      );
    case 'gemini-cli':
      return JSON.stringify(
        {
          mcpServers: {
            colabhive: {
              httpUrl: mcpUrl,
              headers: { Authorization: authHeader },
              timeout: 1800000,
            },
          },
        },
        null,
        2,
      );
    case 'windsurf':
      return JSON.stringify(
        {
          mcpServers: {
            colabhive: {
              serverUrl: mcpUrl,
              headers: { Authorization: authHeader },
            },
          },
        },
        null,
        2,
      );
    default:
      return JSON.stringify(
        {
          mcpServers: {
            colabhive: {
              url: mcpUrl,
              headers: { Authorization: authHeader },
            },
          },
        },
        null,
        2,
      );
  }
}

const TARGET_TABS: {
  id: ConnectTargetTab;
  label: string;
  monogram: string;
  badge: string;
  isSdkOrCli?: boolean;
}[] = [
  { id: 'antigravity', label: 'Antigravity', monogram: 'AG', badge: 'MCP HTTP' },
  { id: 'claude-code', label: 'Claude Code', monogram: 'CC', badge: 'MCP HTTP' },
  { id: 'claude-desktop', label: 'Claude Desktop', monogram: 'CD', badge: 'STDIO' },
  { id: 'codex', label: 'Codex', monogram: 'CX', badge: 'TOML' },
  { id: 'gemini-cli', label: 'Gemini CLI', monogram: 'GM', badge: 'JSON' },
  { id: 'cursor', label: 'Cursor', monogram: 'CU', badge: 'MCP' },
  { id: 'windsurf', label: 'Windsurf', monogram: 'WS', badge: 'MCP' },
  { id: 'opencode', label: 'OpenCode', monogram: 'OC', badge: 'JSONC' },
  { id: 'colab-cli', label: 'colab CLI', monogram: '>_', badge: 'CLI', isSdkOrCli: true },
  { id: 'python-sdk', label: 'Python SDK', monogram: 'PY', badge: 'SDK', isSdkOrCli: true },
];

const AUTO_MINT_KEY_OPTION = '__auto_mint__';

// Shared with `QuickStartLaunchpad`: step 2 ("connect your AI assistant") reads
// this flag and listens for this event to update live.
const AGENT_CONNECTED_STORAGE_KEY = 'colabhive_agent_connected_v3';
const AGENT_CONNECTED_EVENT = 'colabhive:agent-connected';

/** Records whether an AI assistant is connected and tells the launchpad right away. */
function announceAgentConnected(connected: boolean): void {
  try {
    localStorage.setItem(AGENT_CONNECTED_STORAGE_KEY, String(connected));
  } catch {
    // Ignore storage errors (private browsing)
  }
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent(AGENT_CONNECTED_EVENT, { detail: { connected } }));
  }
}

/**
 * Returns a real unmasked `ch_sk_...` raw key, or `""` for localhost open-access.
 * Never returns truncated prefixes (`key_prefix`) or synthesized `_live_token` placeholders.
 */
function resolveUsableRawKey(
  candidate: string | undefined | null,
  knownKeys: AgentAPIKey[],
): string {
  if (!candidate || candidate === AUTO_MINT_KEY_OPTION) return '';
  const cleaned = sanitizeAgentApiKey(candidate);
  if (!cleaned) return '';
  // Reject stored prefixes where `GET /api/v1/keys` omitted `raw_key`
  if (knownKeys.some((k) => k.key_prefix === cleaned && !k.raw_key)) {
    return '';
  }
  if (knownKeys.some((k) => Boolean(k.raw_key) && k.raw_key === cleaned)) {
    return cleaned;
  }
  // Full unmasked ColabHive keys (`ch_sk_live_<hex...>`) are >= 24 chars
  if (cleaned.startsWith('ch_sk_') && cleaned.length >= 24) {
    return cleaned;
  }
  return '';
}

/** `initial` may be a newly-created full `raw_key` or a `key_prefix` (see SettingsPage). */
function resolveInitialKey(initial: string, keys: AgentAPIKey[]): string {
  const matchWithRaw = keys.find(
    (k) => Boolean(k.raw_key) && (k.raw_key === initial || k.key_prefix === initial),
  );
  if (matchWithRaw?.raw_key) {
    return resolveUsableRawKey(matchWithRaw.raw_key, keys);
  }
  return resolveUsableRawKey(initial, keys);
}

function configFileName(path: string): string {
  const parts = path.split(/[\\/]/);
  return parts[parts.length - 1] || path;
}

function configNodePath(clientId: AgentClientID): string {
  if (clientId === 'codex') return 'mcp_servers.colabhive';
  if (clientId === 'opencode') return 'mcp.colabhive';
  return 'mcpServers.colabhive';
}

// ============================================================================
// SyntaxCodeBlock — dependency-free token highlighter
// ============================================================================

export type CodeLanguage = 'json' | 'jsonc' | 'toml' | 'bash' | 'powershell' | 'python';

type TokenType =
  | 'plain'
  | 'key'
  | 'string'
  | 'number'
  | 'literal'
  | 'comment'
  | 'keyword'
  | 'section'
  | 'variable'
  | 'function'
  | 'flag'
  | 'punct';

interface Token {
  type: TokenType;
  text: string;
}

/** Rules are tried in order at each position; every pattern must be sticky (`y`). */
type TokenRule = [type: TokenType, pattern: RegExp];

const JSON_RULES: TokenRule[] = [
  ['key', /"(?:[^"\\\n]|\\.)*"(?=\s*:)/y],
  ['string', /"(?:[^"\\\n]|\\.)*"/y],
  ['comment', /\/\/[^\n]*|\/\*[\s\S]*?\*\//y],
  ['number', /-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b/y],
  ['literal', /\b(?:true|false|null)\b/y],
  ['punct', /[{}[\],:]/y],
];

const TOML_RULES: TokenRule[] = [
  ['comment', /#[^\n]*/y],
  ['section', /^[ \t]*\[\[?[^\]\n]+\]\]?[ \t]*$/my],
  ['string', /"(?:[^"\\\n]|\\.)*"|'[^'\n]*'/y],
  ['key', /[A-Za-z0-9_.-]+(?=\s*=)/y],
  ['literal', /\b(?:true|false)\b/y],
  ['number', /-?\b\d+(?:\.\d+)?\b/y],
  ['punct', /[=[\],{}]/y],
];

const SHELL_RULES: TokenRule[] = [
  ['comment', /(?<=^|\s)#[^\n]*/my],
  ['string', /"(?:[^"\\]|\\.)*"|'[^']*'/y],
  ['variable', /\$env:[A-Za-z_]\w*|\$\{?[A-Za-z_]\w*\}?|[A-Za-z_]\w*(?==)/y],
  ['keyword', /\b(?:export|if|then|else|elif|fi|for|while|done|function|return|source|sudo)\b/y],
  ['function', /(?<=^[ \t]*)[A-Za-z][\w.-]*(?=[ \t]|$)/my],
  ['flag', /(?<=\s)--?[A-Za-z][\w-]*/y],
  ['number', /(?<=\s)\d+(?:\.\d+)?\b/y],
  ['plain', /[A-Za-z_][\w.-]*/y],
  ['punct', /[=|&;]/y],
];

const PYTHON_RULES: TokenRule[] = [
  ['comment', /#[^\n]*/y],
  ['string', /[rRbBfF]{0,2}(?:"""[\s\S]*?"""|'''[\s\S]*?'''|"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*')/y],
  [
    'keyword',
    /\b(?:from|import|as|with|def|class|return|if|elif|else|for|while|in|not|and|or|is|try|except|finally|raise|lambda|pass|yield|async|await)\b/y,
  ],
  ['literal', /\b(?:True|False|None)\b/y],
  ['number', /\b\d+(?:\.\d+)?\b/y],
  ['function', /[A-Za-z_]\w*(?=\()/y],
  ['key', /[A-Za-z_]\w*(?==(?!=))/y],
  ['plain', /[A-Za-z_]\w*/y],
  ['punct', /[()[\]{},.:=]/y],
];

const LANGUAGE_RULES: Record<CodeLanguage, TokenRule[]> = {
  json: JSON_RULES,
  jsonc: JSON_RULES,
  toml: TOML_RULES,
  bash: SHELL_RULES,
  powershell: SHELL_RULES,
  python: PYTHON_RULES,
};

function tokenize(code: string, language: CodeLanguage): Token[] {
  const rules = LANGUAGE_RULES[language];
  const tokens: Token[] = [];
  let plain = '';
  let i = 0;

  const push = (type: TokenType, text: string) => {
    if (type === 'plain') {
      plain += text;
      return;
    }
    if (plain) {
      tokens.push({ type: 'plain', text: plain });
      plain = '';
    }
    tokens.push({ type, text });
  };

  while (i < code.length) {
    let matched = false;
    for (const [type, pattern] of rules) {
      pattern.lastIndex = i;
      const m = pattern.exec(code);
      if (m && m[0].length > 0) {
        push(type, m[0]);
        i += m[0].length;
        matched = true;
        break;
      }
    }
    if (!matched) {
      plain += code[i];
      i += 1;
    }
  }
  if (plain) tokens.push({ type: 'plain', text: plain });
  return tokens;
}

export interface SyntaxCodeBlockProps {
  /** Text to display. Mask secrets before passing it in. */
  code: string;
  language: CodeLanguage;
  /** Top-bar caption: file name, node path, or shell name. */
  label: React.ReactNode;
  /** Text placed on the clipboard. Defaults to `code`. */
  copyText?: string;
  copyLabel?: string;
  copiedLabel?: string;
  /** Called after the text has been placed on the clipboard. */
  onCopied?: () => void;
  className?: string;
}

/** Dark code card with a caption bar, one-click copy, and calm syntax colours. */
export function SyntaxCodeBlock({
  code,
  language,
  label,
  copyText,
  copyLabel = 'Copy',
  copiedLabel = 'Copied',
  onCopied,
  className,
}: SyntaxCodeBlockProps) {
  const [copied, setCopied] = useState(false);
  const tokens = useMemo(() => tokenize(code, language), [code, language]);

  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1800);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const handleCopy = async () => {
    const text = copyText ?? code;
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement('textarea');
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
    }
    setCopied(true);
    onCopied?.();
  };

  return (
    <div className={cn('ui-code', className)}>
      <div
        className="flex items-center justify-between gap-3 h-9 pl-3.5 pr-1.5 border-b"
        style={{
          borderColor: 'rgba(255, 255, 255, 0.06)',
          backgroundColor: 'rgba(255, 255, 255, 0.025)',
        }}
      >
        <div className="ch-num min-w-0 truncate text-[11px]" style={{ color: 'var(--code-muted)' }}>
          {label}
        </div>
        <button
          type="button"
          onClick={handleCopy}
          className="inline-flex items-center gap-1.5 h-6 px-2 shrink-0 rounded-md text-[11px] font-medium cursor-pointer transition-colors hover:bg-white/[0.07]"
          style={{ color: copied ? 'var(--code-string)' : 'var(--code-fg)' }}
        >
          {copied ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
          <span>{copied ? copiedLabel : copyLabel}</span>
        </button>
      </div>
      <pre className="px-3.5 py-3 overflow-x-auto">
        <code>
          {tokens.map((token, idx) => (
            <span key={idx} className={token.type === 'plain' ? undefined : `tok-${token.type}`}>
              {token.text}
            </span>
          ))}
        </code>
      </pre>
    </div>
  );
}

function CodeLabel({ primary, secondary }: { primary: string; secondary?: string }) {
  return (
    <>
      <span style={{ color: 'var(--code-fg)' }}>{primary}</span>
      {secondary && <span> › {secondary}</span>}
    </>
  );
}

// ============================================================================
// Small layout helpers
// ============================================================================

function Field({
  label,
  htmlFor,
  right,
  children,
}: {
  label: React.ReactNode;
  htmlFor?: string;
  right?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="min-w-0">
      <div className="mb-1.5 flex items-center justify-between gap-2 min-h-6">
        <label htmlFor={htmlFor} className="text-[11.5px] font-medium text-fg-secondary">
          {label}
        </label>
        {right}
      </div>
      {children}
    </div>
  );
}

function PathRow({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
}) {
  return (
    <div
      className="flex items-start gap-2.5 px-2.5 py-2 rounded-lg border"
      style={{ backgroundColor: 'var(--surface-0)', borderColor: 'var(--border-faint)' }}
    >
      <span className="mt-px shrink-0 text-fg-muted [&>svg]:w-3.5 [&>svg]:h-3.5">{icon}</span>
      <span className="w-11 shrink-0 text-[11px] font-medium text-fg-muted">{label}</span>
      <span className="ch-num min-w-0 break-all text-[11.5px] leading-relaxed text-fg">{value}</span>
    </div>
  );
}

// ============================================================================
// AgentConnectDrawer
// ============================================================================

export function AgentConnectDrawer({
  open,
  onClose,
  initialKeyPrefix,
  deploymentMode,
  publicUrl,
}: AgentConnectDrawerProps) {
  const { locale, t, privacyMask, displaySecret } = useI18n();
  const zh = locale === 'zh-CN';

  const [selectedTab, setSelectedTab] = useState<ConnectTargetTab>('antigravity');
  const [transport, setTransport] = useState<Transport>('http');
  const [includeWsl2, setIncludeWsl2] = useState<boolean>(true);
  // Address, key and transport are tucked away: the defaults suit most people.
  const [settingsOpen, setSettingsOpen] = useState<boolean>(false);
  const [serverBaseUrl, setServerBaseUrl] = useState<string>(() =>
    (
      publicUrl ||
      (typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860')
    ).replace(/\/$/, ''),
  );
  const [remoteUrlCopied, setRemoteUrlCopied] = useState(false);

  const [keys, setKeys] = useState<AgentAPIKey[]>([]);
  const [selectedKeyValue, setSelectedKeyValue] = useState<string>('');
  const [newlyMintedRawKey, setNewlyMintedRawKey] = useState<string | null>(null);

  const [clients, setClients] = useState<AgentClientConfigStatus[]>([]);
  const [clientsFetchedForKey, setClientsFetchedForKey] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [busyAction, setBusyAction] = useState<'inject' | 'restore' | 'mint' | null>(null);
  const [toast, setToast] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const isCloudMode =
    deploymentMode === 'cloud' ||
    Boolean(clients[0]?.cloud_mode || clients[0]?.supports_auto_inject === false);

  useEffect(() => {
    if (!open) return;
    if (publicUrl && publicUrl.trim()) {
      setServerBaseUrl(publicUrl.trim().replace(/\/$/, ''));
    } else if (typeof window !== 'undefined') {
      setServerBaseUrl(window.location.origin.replace(/\/$/, ''));
    }
  }, [open, publicUrl]);

  useEffect(() => {
    if (isCloudMode && transport !== 'http') {
      setTransport('http');
    }
  }, [isCloudMode, transport]);

  // Read through a ref so that typing in the form does not re-trigger the scan.
  const latestForm = useRef({ selectedKeyValue, serverBaseUrl, transport });
  latestForm.current = { selectedKeyValue, serverBaseUrl, transport };

  const drawerLoadSeqRef = useRef(0);
  const clientsReqSeqRef = useRef(0);
  const clientsAbortRef = useRef<AbortController | null>(null);

  const fetchClientsGuarded = useCallback(
    async (
      keyForServer: string,
      fallbackOpts?: { serverBaseUrl: string; transport: Transport },
    ) => {
      clientsAbortRef.current?.abort();
      const controller = new AbortController();
      clientsAbortRef.current = controller;
      const reqSeq = ++clientsReqSeqRef.current;

      try {
        const list = await api.listAgentClients(keyForServer || undefined, controller.signal);
        if (controller.signal.aborted || reqSeq !== clientsReqSeqRef.current) {
          return;
        }
        if (list && list.length > 0) {
          setClients(list);
          setClientsFetchedForKey(keyForServer);
        } else if (fallbackOpts) {
          setClients(
            buildFallbackClientConfigs(
              fallbackOpts.serverBaseUrl,
              keyForServer,
              fallbackOpts.transport,
            ),
          );
          setClientsFetchedForKey(keyForServer);
        }
      } catch {
        if (controller.signal.aborted || reqSeq !== clientsReqSeqRef.current) {
          return;
        }
        if (fallbackOpts) {
          setClients(
            buildFallbackClientConfigs(
              fallbackOpts.serverBaseUrl,
              keyForServer,
              fallbackOpts.transport,
            ),
          );
          setClientsFetchedForKey(keyForServer);
        }
      }
    },
    [],
  );

  const loadDrawerData = useCallback(
    async (opts: { overrideKey?: string; applyInitialKey?: boolean } = {}) => {
      const loadSeq = ++drawerLoadSeqRef.current;
      setLoading(true);
      try {
        const fetchedKeys = await api.listAPIKeys().catch(() => DEMO_API_KEYS);
        if (loadSeq !== drawerLoadSeqRef.current) return;
        setKeys(fetchedKeys);

        const form = latestForm.current;
        const fromInitial =
          opts.applyInitialKey && initialKeyPrefix
            ? resolveInitialKey(initialKeyPrefix, fetchedKeys)
            : '';
        // Default to `""` (Localhost Open Access) unless an unmasked full `ch_sk_...` key was chosen
        const candidateKey =
          opts.overrideKey !== undefined
            ? opts.overrideKey
            : fromInitial || form.selectedKeyValue || '';
        const nextKey =
          candidateKey === AUTO_MINT_KEY_OPTION
            ? AUTO_MINT_KEY_OPTION
            : resolveUsableRawKey(candidateKey, fetchedKeys);
        if (nextKey !== form.selectedKeyValue) setSelectedKeyValue(nextKey);

        const usableForServer = resolveUsableRawKey(nextKey, fetchedKeys);
        await fetchClientsGuarded(usableForServer, {
          serverBaseUrl: form.serverBaseUrl,
          transport: form.transport,
        });
      } finally {
        if (loadSeq === drawerLoadSeqRef.current) {
          setLoading(false);
        }
      }
    },
    [fetchClientsGuarded, initialKeyPrefix],
  );

  useEffect(() => {
    if (!open) {
      drawerLoadSeqRef.current += 1;
      clientsReqSeqRef.current += 1;
      clientsAbortRef.current?.abort();
      return;
    }
    void loadDrawerData({ applyInitialKey: true });
    return () => {
      clientsAbortRef.current?.abort();
    };
  }, [open, loadDrawerData]);

  const usableKey = useMemo(
    () => resolveUsableRawKey(selectedKeyValue, keys),
    [selectedKeyValue, keys],
  );

  // Refresh server-generated client snippets whenever a real unmasked key is selected or cleared
  useEffect(() => {
    if (!open) return;
    if (clientsFetchedForKey === usableKey) return;
    void fetchClientsGuarded(usableKey);
  }, [clientsFetchedForKey, fetchClientsGuarded, open, usableKey]);

  const maskKey = useCallback(
    (text: string) =>
      privacyMask && usableKey ? text.split(usableKey).join(displaySecret(usableKey)) : text,
    [displaySecret, usableKey, privacyMask],
  );

  // Live fallback snippets reflecting the chosen transport, gateway URL and sanitized key
  const dynamicFallbackMap = useMemo(() => {
    const list = buildFallbackClientConfigs(serverBaseUrl, usableKey, transport);
    const map = new Map<AgentClientID, AgentClientConfigStatus>();
    for (const c of list) map.set(c.client_id, c);
    return map;
  }, [serverBaseUrl, usableKey, transport]);

  const activeClientStatus = useMemo((): AgentClientConfigStatus | null => {
    if (selectedTab === 'colab-cli' || selectedTab === 'python-sdk') return null;
    const fromServer = clients.find((c) => c.client_id === selectedTab);
    const fallback = dynamicFallbackMap.get(selectedTab)!;
    if (!fromServer) return fallback;

    const defaultOrigin =
      typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860';
    const configuredPublic = (publicUrl || '').trim().replace(/\/$/, '');
    const cleanServerBase = serverBaseUrl.replace(/\/$/, '');
    const effectiveTransport: Transport =
      selectedTab === 'claude-desktop' ? 'stdio' : transport;
    const sameGateway =
      cleanServerBase === defaultOrigin.replace(/\/$/, '') ||
      (Boolean(configuredPublic) && cleanServerBase === configuredPublic);
    const keySynchronized = clientsFetchedForKey === usableKey;
    // Prefer `fromServer.snippet` from `/api/v1/agents/clients` once synchronized for the active key
    const preferServerSnippet =
      keySynchronized &&
      Boolean(fromServer.snippet) &&
      fromServer.transport === effectiveTransport &&
      sameGateway;
    const cleanDeepLink =
      fromServer.deep_link_uri && !fromServer.deep_link_uri.startsWith('colabhive://')
        ? fromServer.deep_link_uri
        : undefined;

    return {
      ...fallback,
      ...fromServer,
      deep_link_uri: cleanDeepLink,
      snippet: preferServerSnippet ? fromServer.snippet : fallback.snippet,
      cli_command:
        (keySynchronized && sameGateway && fromServer.cli_command) || fallback.cli_command,
    };
  }, [
    clients,
    clientsFetchedForKey,
    dynamicFallbackMap,
    publicUrl,
    selectedTab,
    serverBaseUrl,
    transport,
    usableKey,
  ]);

  const handleQuickMintKey = useCallback(async () => {
    setBusyAction('mint');
    setToast(null);
    try {
      const created = await api.createAPIKey({
        name: `Agent-Connect-${new Date().toISOString().slice(5, 16).replace('T', '-')}`,
        default_strategy: 'waste-zero',
        max_budget_cu_daily: 50,
        max_budget_cu_total: 250,
        max_concurrent_vms: 4,
        idle_auto_kill_min: 20,
      });
      const raw = sanitizeAgentApiKey(created.raw_key);
      if (raw) {
        setNewlyMintedRawKey(raw);
        setSelectedKeyValue(raw);
      }
      setKeys((prev) => [created, ...prev]);
      setToast({
        kind: 'ok',
        text: zh
          ? `已新建密钥「${created.name}」并自动选中`
          : `Created key "${created.name}" and selected it`,
      });
    } catch (err) {
      setToast({
        kind: 'err',
        text:
          err instanceof Error
            ? err.message
            : zh
              ? '新建密钥失败，请稍后再试'
              : 'Could not create a key. Please try again.',
      });
    } finally {
      setBusyAction(null);
    }
  }, [zh]);

  const handleInject = useCallback(async () => {
    if (!activeClientStatus) return;
    setBusyAction('inject');
    setToast(null);
    try {
      let keyForInject = usableKey;
      if (selectedKeyValue === AUTO_MINT_KEY_OPTION) {
        const created = await api.createAPIKey({
          name: `${activeClientStatus.display_name}-${new Date().toISOString().slice(5, 16).replace('T', '-')}`,
          default_strategy: 'waste-zero',
          max_budget_cu_daily: 50,
          max_budget_cu_total: 250,
          max_concurrent_vms: 4,
          idle_auto_kill_min: 20,
        });
        const mintedRaw = sanitizeAgentApiKey(created.raw_key);
        if (mintedRaw) {
          keyForInject = mintedRaw;
          setNewlyMintedRawKey(mintedRaw);
          setSelectedKeyValue(mintedRaw);
        }
        setKeys((prev) => [created, ...prev]);
      }

      const updated = await api.injectAgentConfig({
        client_id: activeClientStatus.client_id,
        server_base_url: serverBaseUrl,
        ...(keyForInject ? { api_key: keyForInject } : {}),
        transport:
          activeClientStatus.client_id === 'claude-desktop' ? 'stdio' : transport,
        include_wsl2: includeWsl2,
      });
      setClients((prev) => {
        const exists = prev.some((c) => c.client_id === updated.client_id);
        if (!exists) return [...prev, updated];
        return prev.map((c) => (c.client_id === updated.client_id ? updated : c));
      });
      // Only a write the local service confirmed counts as "connected".
      if (updated.injected) announceAgentConnected(true);
      setToast({
        kind: 'ok',
        text: zh
          ? `已连上 ${updated.display_name}。配置已写入 ${updated.config_path}，原文件已自动备份。`
          : `${updated.display_name} is connected. Config written to ${updated.config_path}; the original was backed up.`,
      });
    } catch (err) {
      // Nothing was written (usually the local service is not running). Leave
      // the assistant shown as not connected and say plainly what to do next.
      const reason = err instanceof Error ? err.message : '';
      setToast({
        kind: 'err',
        text: zh
          ? `没能自动写入${reason ? `（${reason}）` : ''}。本地服务可能还没启动，你也可以复制下方配置，手动粘贴到配置文件里。`
          : `Could not write the config automatically${reason ? ` (${reason})` : ''}. The local service may not be running. You can copy the config below and paste it in by hand.`,
      });
    } finally {
      setBusyAction(null);
    }
  }, [activeClientStatus, includeWsl2, selectedKeyValue, serverBaseUrl, transport, usableKey, zh]);

  const handleRestore = useCallback(async () => {
    if (!activeClientStatus) return;
    setBusyAction('restore');
    setToast(null);
    try {
      const updated = await api.removeAgentConfig(activeClientStatus.client_id);
      setClients((prev) =>
        prev.map((c) => (c.client_id === updated.client_id ? updated : c)),
      );
      // The last assistant was disconnected: step 2 of the Quick Start is open again.
      const anyStillConnected = clients.some((c) =>
        c.client_id === updated.client_id ? updated.injected : c.injected,
      );
      if (!anyStillConnected) announceAgentConnected(false);
      setToast({
        kind: 'ok',
        text: zh
          ? `已断开 ${updated.display_name}，并还原了它原来的配置。`
          : `${updated.display_name} is disconnected and its original config is restored.`,
      });
    } catch (err) {
      // The restore did not happen: keep showing the state we last knew.
      setToast({
        kind: 'err',
        text:
          err instanceof Error
            ? err.message
            : zh
              ? '还原失败，请稍后再试'
              : 'Could not restore. Please try again.',
      });
    } finally {
      setBusyAction(null);
    }
  }, [activeClientStatus, clients, zh]);

  // Copying a config to paste by hand is the manual way to connect.
  const handleConfigCopied = useCallback(() => announceAgentConnected(true), []);

  if (!open) return null;

  const cleanPublicUrl = serverBaseUrl.replace(/\/$/, '');
  const remoteMcpEndpoint = `${cleanPublicUrl}/mcp`;
  const cloudKeyTokenForPreview = usableKey || '<YOUR_CH_SK_KEY>';

  const copyRemoteMcpUrl = async () => {
    try {
      await navigator.clipboard.writeText(remoteMcpEndpoint);
    } catch {
      const ta = document.createElement('textarea');
      ta.value = remoteMcpEndpoint;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
    }
    setRemoteUrlCopied(true);
    window.setTimeout(() => setRemoteUrlCopied(false), 1800);
  };

  const cliKeyForSnippet = isCloudMode ? cloudKeyTokenForPreview : usableKey;

  const cliBashSnippet = `# Bash / Zsh / WSL2 (100% compatible with official google-colab-cli + sharding)
export COLABHIVE_URL="${cleanPublicUrl}"${
    cliKeyForSnippet ? `\nexport COLABHIVE_API_KEY="${cliKeyForSnippet}"` : ''
  }

# Check pooled quota & GPU-hour equivalence across all accounts
colabhive usage --gpu-hours

# Run a 4-GPU parallel sharded benchmark across 4 pooled accounts (4x faster, 0 extra CU!)
colabhive run --gpu G4 --shards 4 --strategy waste-zero eval_qwen27b.py`;

  const cliPwshSnippet = `# Windows PowerShell
$env:COLABHIVE_URL = "${cleanPublicUrl}"${
    cliKeyForSnippet ? `\n$env:COLABHIVE_API_KEY = "${cliKeyForSnippet}"` : ''
  }

colabhive run --gpu A100 --high-mem train_lora.py`;

  const pythonSdkSnippet = `from colabhive import ColabHiveClient

client = ColabHiveClient(
    base_url="${cleanPublicUrl}",${cliKeyForSnippet ? `\n    api_key="${cliKeyForSnippet}",` : ''}
)

# 1. Stateful Kernel Session (preserves VRAM & 60GB HF cache across calls)
with client.session(gpu="G4", strategy="waste-zero") as rt:
    res = rt.exec("import torch; print(torch.cuda.get_device_name(0))")
    print(res.stdout)

# 2. Multi-Account Parallel Shard Runner (4 GPUs x 2h == 1 GPU x 8h CU cost!)
exp = client.run_sharded(
    script="eval_shard.py",
    shards=4,
    gpu="G4",
    auto_stop_on_complete=True,
    wait=True,
)
print(exp.status, exp.merged_jsonl_path)`;

  const copyLabel = t('common.copy');
  const copiedLabel = t('common.copied');

  const isDesktop = selectedTab === 'claude-desktop' && !isCloudMode;
  const syncedCount = clients.filter((c) => c.injected).length;

  // Guide strip: adapts cleanly between Local 1-Click Inject and Cloud Remote MCP!
  const guideStep = isCloudMode ? (usableKey ? 2 : 1) : activeClientStatus?.injected ? 2 : 1;
  const guideSteps = isCloudMode
    ? [
        zh ? '选择 AI 助手' : 'Pick assistant',
        zh ? '签发独立 ch_sk_… 密钥' : 'Mint ch_sk_… key',
        zh ? '本地运行命令或复制配置' : 'Run laptop CLI or copy config',
      ]
    : [t('connect.step_pick'), t('connect.step_inject'), t('connect.step_use')];

  const unmaskedKeyOptions = keys
    .filter((k) => Boolean(sanitizeAgentApiKey(k.raw_key)))
    .map((k) => {
      const value = sanitizeAgentApiKey(k.raw_key);
      const isNew = newlyMintedRawKey !== null && value === newlyMintedRawKey;
      return {
        id: k.id,
        value,
        label: `${isNew ? (zh ? '刚新建 · ' : 'New · ') : ''}${k.name} · ${k.key_prefix}… · ${
          k.default_strategy ?? 'waste-zero'
        } · ${k.max_budget_cu_daily} CU/d`,
      };
    });
  const maskedExistingKeys = keys.filter((k) => !sanitizeAgentApiKey(k.raw_key));

  const mintedListed =
    newlyMintedRawKey !== null &&
    unmaskedKeyOptions.some((o) => o.value === newlyMintedRawKey);
  const selectedInSelect =
    selectedKeyValue === '' ||
    selectedKeyValue === AUTO_MINT_KEY_OPTION ||
    unmaskedKeyOptions.some((o) => o.value === selectedKeyValue) ||
    (newlyMintedRawKey !== null && selectedKeyValue === newlyMintedRawKey);

  const selectedKeyName =
    selectedKeyValue === ''
      ? isCloudMode
        ? zh
          ? '待签发独立 ch_sk_… 密钥'
          : 'Mint a ch_sk_… key'
        : zh
          ? '本机免密直连（推荐）'
          : 'Localhost Open Access (recommended)'
      : selectedKeyValue === AUTO_MINT_KEY_OPTION
        ? zh
          ? '接入时自动新建密钥'
          : 'Auto-mint key on connect'
        : unmaskedKeyOptions.find((o) => o.value === selectedKeyValue)?.label.split(' · ')[0] ??
          (usableKey
            ? zh
              ? `独立密钥 (${displaySecret(usableKey)})`
              : `Scoped key (${displaySecret(usableKey)})`
            : isCloudMode
              ? zh
                ? '待签发独立 ch_sk_… 密钥'
                : 'Mint a ch_sk_… key'
              : zh
                ? '本机免密直连（推荐）'
                : 'Localhost Open Access (recommended)');

  return (
    <SlideOver
      open={open}
      onClose={onClose}
      width="xl"
      icon={isCloudMode ? <Cloud /> : <Zap />}
      title={
        isCloudMode
          ? zh
            ? '云端远程网关接入 (Cloud Remote MCP)'
            : 'Cloud Remote MCP Connect'
          : t('connect.title')
      }
      subtitle={
        isCloudMode
          ? zh
            ? '签发独立 ch_sk_… 受限密钥，在本地开发机通过一行 CLI 命令或 HTTP 配置连接此云端算力池'
            : 'Mint a scoped ch_sk_… key and connect your local laptop IDE via 1-line CLI or Streamable HTTP'
          : t('connect.subtitle')
      }
      headerActions={
        <IconButton
          label={
            isCloudMode
              ? zh
                ? '刷新密钥与网关配置'
                : 'Refresh keys & gateway snippets'
              : zh
                ? '重新检测本机已安装的助手'
                : 'Re-check assistants on this machine'
          }
          onClick={() => loadDrawerData({ overrideKey: selectedKeyValue })}
        >
          <RefreshCw className={cn('w-4 h-4', loading && 'animate-spin')} />
        </IconButton>
      }
      className="space-y-5"
    >
      {/* ------------------------------------------------------------------ */}
      {/* 0. Three-step guide: pick → connect → just ask in chat              */}
      {/* ------------------------------------------------------------------ */}
      <ol
        aria-label={zh ? '接入步骤' : 'How it works'}
        className="ui-well grid grid-cols-1 sm:grid-cols-3 gap-x-2 gap-y-2 p-3"
      >
        {guideSteps.map((text, i) => {
          const done = i < guideStep;
          const current = i === guideStep;
          return (
            <li
              key={text}
              aria-current={current ? 'step' : undefined}
              className="flex items-center gap-2 min-w-0"
            >
              <span
                className="ch-num flex items-center justify-center w-5 h-5 shrink-0 rounded-full text-[10.5px] font-semibold transition-colors duration-200"
                style={
                  done
                    ? { color: '#ffffff', backgroundColor: 'var(--emerald)' }
                    : current
                      ? {
                          color: 'var(--accent-fg)',
                          backgroundColor: 'var(--accent)',
                          boxShadow: 'var(--shadow-accent)',
                        }
                      : { color: 'var(--text-muted)', backgroundColor: 'var(--neutral-soft)' }
                }
              >
                {done ? <Check className="w-3 h-3" strokeWidth={3} /> : i + 1}
              </span>
              <span
                className={cn(
                  'flex-1 min-w-0 text-[12px] leading-snug',
                  current ? 'font-medium text-fg' : 'text-fg-secondary',
                )}
              >
                {text}
              </span>
              {i < guideSteps.length - 1 && (
                <ChevronRight
                  className="hidden sm:block w-3 h-3 shrink-0 text-fg-muted"
                  aria-hidden="true"
                />
              )}
            </li>
          );
        })}
      </ol>

      {/* ------------------------------------------------------------------ */}
      {/* 1. Target selector                                                  */}
      {/* ------------------------------------------------------------------ */}
      <section className="space-y-2.5">
        <div className="flex items-center justify-between gap-2 min-h-5">
          <h3 className="text-[12.5px] font-semibold text-fg">
            {zh ? '你在用哪个 AI 编程助手？' : 'Which AI coding assistant do you use?'}
          </h3>
          {syncedCount > 0 && (
            <StatusPill tone="emerald" size="xs" dot>
              {zh ? `已接入 ${syncedCount} 个` : `${syncedCount} connected`}
            </StatusPill>
          )}
        </div>

        <div
          role="radiogroup"
          aria-label={zh ? 'AI 编程助手' : 'AI coding assistant'}
          className="ui-stagger grid grid-cols-2 min-[420px]:grid-cols-3 sm:grid-cols-5 gap-1.5"
        >
          {TARGET_TABS.map((tab) => {
            const isSelected = selectedTab === tab.id;
            const statusObj = !tab.isSdkOrCli
              ? clients.find((c) => c.client_id === tab.id)
              : undefined;
            const isInjected = Boolean(statusObj?.injected);

            return (
              <button
                key={tab.id}
                type="button"
                role="radio"
                aria-checked={isSelected}
                title={tab.label}
                onClick={() => {
                  setSelectedTab(tab.id);
                  setToast(null);
                }}
                className={cn(
                  'relative flex flex-col items-center gap-1.5 min-w-0 px-1 pt-3 pb-2.5 rounded-xl border text-center cursor-pointer',
                  'transition-[background-color,border-color,box-shadow,transform] duration-200 active:scale-[0.97]',
                  isSelected
                    ? 'border-[var(--accent)] bg-[var(--surface-2)] shadow-[var(--shadow-card)]'
                    : 'border-line bg-[var(--control-bg)] hover:border-line-strong hover:bg-[var(--control-bg-hover)] hover:-translate-y-px',
                )}
              >
                {isInjected && (
                  <CheckCircle2
                    className="absolute top-1.5 right-1.5 w-3.5 h-3.5"
                    style={{ color: 'var(--emerald)' }}
                  />
                )}
                <span
                  className="ch-num flex items-center justify-center w-8 h-8 rounded-[10px] border text-[11px] font-semibold transition-colors"
                  style={
                    isSelected
                      ? {
                          backgroundColor: 'var(--accent)',
                          backgroundImage: 'var(--accent-sheen)',
                          borderColor: 'transparent',
                          color: 'var(--accent-fg)',
                          boxShadow: 'var(--shadow-accent)',
                        }
                      : {
                          backgroundColor: 'var(--surface-2)',
                          borderColor: 'var(--border-subtle)',
                          color: 'var(--text-secondary)',
                          boxShadow: 'var(--shadow-control)',
                        }
                  }
                >
                  {tab.monogram}
                </span>
                <span className="max-w-full truncate text-[11px] font-medium leading-tight text-fg">
                  {tab.label}
                </span>
                <span
                  className="ch-num text-[9.5px] leading-none"
                  style={{ color: isInjected ? 'var(--emerald-text)' : 'var(--text-muted)' }}
                >
                  {isInjected
                    ? zh
                      ? '已接入'
                      : 'Connected'
                    : isCloudMode && tab.id === 'claude-desktop'
                      ? 'HTTP'
                      : tab.badge}
                </span>
              </button>
            );
          })}
        </div>
      </section>

      {/* ------------------------------------------------------------------ */}
      {/* 2. Connection settings (Cloud prominent key mint vs Local defaults) */}
      {/* ------------------------------------------------------------------ */}
      {isCloudMode ? (
        <InsetWell className="space-y-3.5">
          <div className="flex flex-wrap items-start justify-between gap-2.5">
            <div className="min-w-0 space-y-0.5">
              <div className="flex items-center gap-2">
                <span className="text-[13px] font-semibold text-fg">
                  {zh
                    ? '自动签发独立 ch_sk_… 密钥 (Scoped Agent Key)'
                    : 'Mint Scoped ch_sk_… Agent Key'}
                </span>
                {usableKey ? (
                  <StatusPill tone="emerald" size="xs" dot>
                    {zh ? '密钥已就绪' : 'Key Ready'}
                  </StatusPill>
                ) : (
                  <StatusPill tone="amber" size="xs" dot>
                    {zh ? '云端必选' : 'Required for Cloud'}
                  </StatusPill>
                )}
              </div>
              <p className="text-[12px] leading-relaxed text-fg-secondary">
                {zh
                  ? 'AI 助手仅持有受限 ch_sk_… 密钥（仅可调用远程算力与查看额度，无法导出保险箱或管理账号池）。'
                  : 'Scoped ch_sk_… keys grant MCP compute & quota access only — never admin or vault access.'}
              </p>
            </div>

            <ActionButton
              variant={usableKey ? 'secondary' : 'primary'}
              size="sm"
              icon={<KeyRound />}
              loading={busyAction === 'mint'}
              disabled={busyAction !== null}
              onClick={handleQuickMintKey}
            >
              {usableKey
                ? zh
                  ? '+ 再签发一把新密钥'
                  : '+ Mint Another Key'
                : zh
                  ? '自动签发独立 ch_sk_… 密钥'
                  : '1-Click Mint ch_sk_… Key'}
            </ActionButton>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
            <Field
              label={zh ? '选择或粘贴完整 ch_sk_… 密钥' : 'Select or paste full ch_sk_… key'}
              htmlFor="cloud-connect-key-input"
            >
              <input
                id="cloud-connect-key-input"
                type={privacyMask ? 'password' : 'text'}
                value={selectedKeyValue === AUTO_MINT_KEY_OPTION ? '' : selectedKeyValue}
                onChange={(e) => setSelectedKeyValue(e.target.value)}
                placeholder={
                  zh
                    ? '点击右上角一键签发，或粘贴已有 ch_sk_…'
                    : 'Click Mint above or paste ch_sk_…'
                }
                autoComplete="off"
                spellCheck={false}
                className="ui-input ch-num h-9 w-full"
              />
            </Field>

            <Field
              label={zh ? '云端公网网关地址 (Public URL)' : 'Cloud Public Gateway URL'}
              htmlFor="cloud-connect-public-url"
            >
              <input
                id="cloud-connect-public-url"
                type="text"
                value={serverBaseUrl}
                onChange={(e) => setServerBaseUrl(e.target.value)}
                spellCheck={false}
                className="ui-input ch-num h-9 w-full"
              />
            </Field>
          </div>
        </InsetWell>
      ) : (
        <div className="ui-well min-w-0">
          <button
            type="button"
            aria-expanded={settingsOpen}
            aria-controls="connect-settings-panel"
            onClick={() => setSettingsOpen((v) => !v)}
            className="flex items-center gap-2.5 w-full px-3.5 py-2.5 rounded-xl text-left cursor-pointer transition-colors hover:bg-[var(--hover-fill)]"
          >
            <SlidersHorizontal className="w-3.5 h-3.5 shrink-0 text-fg-muted" />
            <span className="flex-1 min-w-0">
              <span className="block text-[12.5px] font-medium text-fg">
                {zh ? '连接设置' : 'Connection settings'}
                <span className="ml-1.5 font-normal text-fg-muted">
                  {zh ? '一般保持默认就行' : 'Defaults work for most setups'}
                </span>
              </span>
              <span className="ch-num block truncate text-[11px] text-fg-muted">
                {serverBaseUrl} · {selectedKeyName}
              </span>
            </span>
            <ChevronDown
              className={cn(
                'w-4 h-4 shrink-0 text-fg-muted transition-transform duration-200',
                settingsOpen && 'rotate-180',
              )}
            />
          </button>

          {/* Kept mounted (`hidden`) so typed values survive collapsing the panel. */}
          <div
            id="connect-settings-panel"
            hidden={!settingsOpen}
            className="ui-anim-fade space-y-4 px-3.5 pb-3.5 pt-1 sm:px-4 sm:pb-4"
          >
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <Field
                label={zh ? 'ColabHive 服务地址' : 'ColabHive service URL'}
                htmlFor="connect-gateway-url"
              >
                <input
                  id="connect-gateway-url"
                  type="text"
                  value={serverBaseUrl}
                  onChange={(e) => setServerBaseUrl(e.target.value)}
                  spellCheck={false}
                  className="ui-input ch-num h-9"
                />
              </Field>

              <Field label={zh ? '连接方式' : 'Connection type'}>
                <SegmentedTabs<Transport>
                  size="sm"
                  fullWidth
                  aria-label={zh ? '连接方式' : 'Connection type'}
                  value={isDesktop ? 'stdio' : transport}
                  onChange={setTransport}
                  options={[
                    {
                      value: 'http',
                      label: zh ? 'HTTP（推荐）' : 'HTTP (recommended)',
                      disabled: isDesktop,
                    },
                    { value: 'stdio', label: 'stdio' },
                  ]}
                />
              </Field>
            </div>

            <Field
              label={
                <span className="inline-flex items-center gap-1.5">
                  <KeyRound className="w-3 h-3" />
                  {zh ? '访问密钥 (ch_sk_…)' : 'Access key (ch_sk_…)'}
                </span>
              }
              htmlFor="connect-api-key"
              right={
                <ActionButton
                  variant="ghost"
                  size="xs"
                  icon={<Plus />}
                  loading={busyAction === 'mint'}
                  disabled={busyAction !== null}
                  onClick={handleQuickMintKey}
                  style={{ color: 'var(--accent-text)' }}
                >
                  {zh ? '新建一个密钥' : 'New Key'}
                </ActionButton>
              }
            >
              <div className="flex flex-col sm:flex-row gap-2">
                <select
                  id="connect-api-key"
                  value={selectedKeyValue}
                  onChange={(e) => setSelectedKeyValue(e.target.value)}
                  className="ui-input ch-num h-9 sm:flex-1 min-w-0"
                >
                  <option value="">
                    {zh ? '本机免密直连（推荐）' : 'Localhost Open Access (recommended)'}
                  </option>
                  <option value={AUTO_MINT_KEY_OPTION}>
                    {zh ? '接入时自动新建独立密钥' : 'Auto-mint new key on connect'}
                  </option>
                  {newlyMintedRawKey !== null && !mintedListed && (
                    <option value={newlyMintedRawKey}>
                      {zh ? '刚新建的密钥' : 'Newly created'} · {displaySecret(newlyMintedRawKey)}
                    </option>
                  )}
                  {unmaskedKeyOptions.map((o) => (
                    <option key={o.id} value={o.value}>
                      {o.label}
                    </option>
                  ))}
                  {maskedExistingKeys.map((k) => (
                    <option key={k.id} value={k.key_prefix} disabled>
                      {k.name} · {k.key_prefix} (
                      {zh ? '已脱敏，需在右侧粘贴完整密钥' : 'masked — paste full key on right'})
                    </option>
                  ))}
                  {!selectedInSelect && selectedKeyValue !== '' && (
                    <option value={selectedKeyValue}>{zh ? '自定义密钥' : 'Custom key'}</option>
                  )}
                </select>
                <input
                  type={privacyMask ? 'password' : 'text'}
                  value={selectedKeyValue === AUTO_MINT_KEY_OPTION ? '' : selectedKeyValue}
                  onChange={(e) => setSelectedKeyValue(e.target.value)}
                  placeholder={zh ? '留空免密，或粘贴完整 ch_sk_...' : 'Optional full ch_sk_...'}
                  aria-label={zh ? '自定义密钥' : 'Custom key'}
                  autoComplete="off"
                  spellCheck={false}
                  className="ui-input ch-num h-9 sm:w-48"
                />
              </div>
            </Field>

            <div className="flex items-center justify-between gap-4">
              <div className="flex items-start gap-2.5 min-w-0">
                <Layers className="w-3.5 h-3.5 mt-0.5 shrink-0 text-fg-muted" />
                <div className="min-w-0">
                  <div className="text-[12.5px] font-medium text-fg">
                    {zh ? '同时写入 WSL2 里的配置' : 'Also write the config inside WSL2'}
                  </div>
                  <div className="ch-num mt-0.5 truncate text-[11px] text-fg-muted">
                    {'\\\\wsl.localhost\\<Distro>\\home\\…'}
                  </div>
                </div>
              </div>
              <Switch
                checked={includeWsl2}
                onChange={setIncludeWsl2}
                label={zh ? '同时写入 WSL2 里的配置' : 'Also write the config inside WSL2'}
              />
            </div>
          </div>
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* 3a. AI assistants: Cloud Remote MCP vs Local 1-Click Inject         */}
      {/* ------------------------------------------------------------------ */}
      {activeClientStatus && isCloudMode && (
        <>
          {toast && <ToastBanner toast={toast} />}

          {/* 1) Remote Streamable HTTP URL */}
          <InsetWell className="space-y-2.5">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                <Globe className="w-3.5 h-3.5 text-fg-muted shrink-0" />
                <span className="text-[12.5px] font-semibold text-fg">
                  {zh
                    ? '1. 远程 Streamable HTTP 网关地址'
                    : '1. Remote Streamable HTTP MCP Endpoint'}
                </span>
              </div>
              <StatusPill size="xs" tone="sky" mono>
                MCP HTTP
              </StatusPill>
            </div>

            <div className="flex items-center gap-2">
              <input
                type="text"
                readOnly
                value={remoteMcpEndpoint}
                onFocus={(e) => e.currentTarget.select()}
                aria-label="Remote Streamable HTTP URL"
                className="ui-input ch-num h-9 flex-1 min-w-0 text-[12px]"
              />
              <ActionButton
                variant="secondary"
                size="sm"
                icon={remoteUrlCopied ? <Check /> : <Copy />}
                onClick={() => void copyRemoteMcpUrl()}
              >
                {remoteUrlCopied ? copiedLabel : copyLabel}
              </ActionButton>
            </div>
          </InsetWell>

          {/* 2) 1-Line Local Laptop CLI Connector Command */}
          <section className="space-y-2">
            <h3 className="text-[12.5px] font-semibold text-fg">
              {zh
                ? `2. 在本地开发机运行一行命令自动接入 ${activeClientStatus.display_name}`
                : `2. 1-Line Local Laptop CLI Command for ${activeClientStatus.display_name}`}
            </h3>
            <SyntaxCodeBlock
              language="bash"
              label={
                <CodeLabel
                  primary="laptop terminal"
                  secondary={zh ? '自动备份并写入本地配置' : 'local surgical config write'}
                />
              }
              code={maskKey(
                `colabhive connect ${activeClientStatus.client_id} --server ${cleanPublicUrl} --key ${cloudKeyTokenForPreview}`,
              )}
              copyText={`colabhive connect ${activeClientStatus.client_id} --server ${cleanPublicUrl} --key ${cloudKeyTokenForPreview}`}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
              onCopied={handleConfigCopied}
            />
          </section>

          {/* 3) Ready-to-copy JSON / Codex TOML / OpenCode JSONC config snippet */}
          <section className="space-y-2">
            <h3 className="text-[12.5px] font-semibold text-fg">
              {zh
                ? `3. 或手动复制粘贴到 ${configFileName(activeClientStatus.config_path)}`
                : `3. Or copy-paste directly into ${configFileName(activeClientStatus.config_path)}`}
            </h3>
            <SyntaxCodeBlock
              language={activeClientStatus.format}
              label={
                <CodeLabel
                  primary={configFileName(activeClientStatus.config_path)}
                  secondary={configNodePath(activeClientStatus.client_id)}
                />
              }
              code={maskKey(
                buildCloudRemoteSnippet(
                  activeClientStatus.client_id,
                  cleanPublicUrl,
                  cloudKeyTokenForPreview,
                ),
              )}
              copyText={buildCloudRemoteSnippet(
                activeClientStatus.client_id,
                cleanPublicUrl,
                cloudKeyTokenForPreview,
              )}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
              onCopied={handleConfigCopied}
            />
            {activeClientStatus.hot_reload_note && (
              <p className="flex items-start gap-1.5 text-[11.5px] leading-relaxed text-fg-muted">
                <Info className="w-3 h-3 mt-[3px] shrink-0" />
                <span>{activeClientStatus.hot_reload_note}</span>
              </p>
            )}
          </section>
        </>
      )}

      {activeClientStatus && !isCloudMode && (
        <>
          <InsetWell>
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
              <h3 className="text-[14px] font-semibold text-fg">
                {activeClientStatus.display_name}
              </h3>
              <StatusPill size="xs" mono className="uppercase">
                {activeClientStatus.format}
              </StatusPill>
              {activeClientStatus.injected ? (
                <StatusPill size="xs" tone="emerald" icon={<CheckCircle2 />}>
                  {zh ? '已接入' : 'Connected'}
                </StatusPill>
              ) : activeClientStatus.detected ? (
                <StatusPill size="xs" tone="info" dot>
                  {zh ? '已找到它的配置文件' : 'Config file found'}
                </StatusPill>
              ) : (
                <StatusPill size="xs" dot>
                  {zh ? '接入时会自动创建配置' : 'Config is created on connect'}
                </StatusPill>
              )}
            </div>

            <div className="mt-3 space-y-1.5">
              <PathRow
                icon={<FolderOpen />}
                label={zh ? '配置' : 'Config'}
                value={activeClientStatus.config_path}
              />
              {includeWsl2 && activeClientStatus.wsl_config_path && (
                <PathRow
                  icon={<Layers />}
                  label="WSL2"
                  value={activeClientStatus.wsl_config_path}
                />
              )}
            </div>

            <div className="mt-2.5">
              <StatusPill
                tone={activeClientStatus.backup_exists ? 'emerald' : 'neutral'}
                icon={<ShieldCheck />}
                title={activeClientStatus.backup_path}
              >
                {activeClientStatus.backup_exists
                  ? t('connect.backup_safe')
                  : zh
                    ? '首次修改前会自动备份原文件'
                    : 'The original file is backed up before the first change'}
              </StatusPill>
            </div>

            <div className="mt-4 flex flex-wrap items-center gap-2">
              <ActionButton
                variant="primary"
                size="md"
                icon={<Zap />}
                loading={busyAction === 'inject'}
                disabled={busyAction !== null}
                onClick={handleInject}
              >
                {activeClientStatus.injected
                  ? zh
                    ? '重新写入配置'
                    : 'Re-Write Config'
                  : t('connect.inject')}
              </ActionButton>

              <ActionButton
                size="md"
                icon={<RotateCcw />}
                loading={busyAction === 'restore'}
                disabled={
                  busyAction !== null ||
                  (!activeClientStatus.injected && !activeClientStatus.backup_exists)
                }
                onClick={handleRestore}
              >
                {zh ? '断开并还原' : 'Disconnect & Restore'}
              </ActionButton>

              {Boolean(activeClientStatus.deep_link_uri?.trim()) && (
                <a
                  href={activeClientStatus.deep_link_uri}
                  className="ui-btn ui-btn-ghost ui-btn-md"
                >
                  <ExternalLink className="w-3.5 h-3.5" />
                  <span>{zh ? '在应用中打开' : 'Open in App'}</span>
                </a>
              )}
            </div>

            {activeClientStatus.injected && (
              <p
                className="ui-anim-fade mt-3 flex items-start gap-1.5 text-[12px] leading-relaxed"
                style={{ color: 'var(--emerald-text)' }}
              >
                <CheckCircle2 className="w-3.5 h-3.5 mt-[2px] shrink-0" />
                <span>
                  {zh
                    ? `下一步：回到 ${activeClientStatus.display_name}，直接说「帮我在 Colab 上跑这段代码」。`
                    : `Next: go back to ${activeClientStatus.display_name} and say "run this code on Colab".`}
                </span>
              </p>
            )}
          </InsetWell>

          {toast && <ToastBanner toast={toast} />}

          <section className="space-y-2.5">
            <h3 className="text-[12px] font-medium text-fg-secondary">
              {zh
                ? '想手动配置？复制下面的内容粘贴进去也可以'
                : 'Prefer to set it up by hand? Copy and paste the config below'}
            </h3>

            {activeClientStatus.cli_command && (
              <SyntaxCodeBlock
                language="bash"
                label={
                  <CodeLabel
                    primary="bash"
                    secondary={zh ? '或者在终端运行这一行' : 'Or run this one line'}
                  />
                }
                code={maskKey(activeClientStatus.cli_command)}
                copyText={activeClientStatus.cli_command}
                copyLabel={copyLabel}
                copiedLabel={copiedLabel}
                onCopied={handleConfigCopied}
              />
            )}

            <SyntaxCodeBlock
              language={activeClientStatus.format}
              label={
                <CodeLabel
                  primary={configFileName(activeClientStatus.config_path)}
                  secondary={configNodePath(activeClientStatus.client_id)}
                />
              }
              code={maskKey(activeClientStatus.snippet)}
              copyText={activeClientStatus.snippet}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
              onCopied={handleConfigCopied}
            />

            {activeClientStatus.hot_reload_note && (
              <p className="flex items-start gap-1.5 text-[11.5px] leading-relaxed text-fg-muted">
                <Info className="w-3 h-3 mt-[3px] shrink-0" />
                <span>{activeClientStatus.hot_reload_note}</span>
              </p>
            )}
          </section>

          <p className="text-[11.5px] leading-relaxed text-fg-muted">
            {zh
              ? '接入后助手一启动就能看到全部算力工具，不需要额外刷新；多个子任务可以同时使用不同的显卡，互不干扰。'
              : 'Once connected, every compute tool is available as soon as the assistant starts, with no extra refresh. Several sub-tasks can use different GPUs at the same time.'}
          </p>
        </>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* 3b. colab CLI                                                       */}
      {/* ------------------------------------------------------------------ */}
      {selectedTab === 'colab-cli' && (
        <>
          <TargetIntro
            icon={<Terminal />}
            title={
              zh
                ? '命令行用法：和官方 colab 命令一样，还能多账号并行'
                : 'Command line: works like the official colab CLI, plus multi-account parallel runs'
            }
            description={
              zh
                ? '先设置下面两个环境变量，之后就能像用官方命令行一样使用 colabhive。'
                : 'Set the two environment variables below, then use colabhive like the official CLI.'
            }
          />
          {toast && <ToastBanner toast={toast} />}
          <section className="space-y-2.5">
            <SyntaxCodeBlock
              language="bash"
              label={<CodeLabel primary="bash" secondary="zsh · WSL2" />}
              code={maskKey(cliBashSnippet)}
              copyText={cliBashSnippet}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
            />
            <SyntaxCodeBlock
              language="powershell"
              label={<CodeLabel primary="powershell" secondary="pwsh" />}
              code={maskKey(cliPwshSnippet)}
              copyText={cliPwshSnippet}
              copyLabel={copyLabel}
              copiedLabel={copiedLabel}
            />
          </section>
        </>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* 3c. Python SDK                                                      */}
      {/* ------------------------------------------------------------------ */}
      {selectedTab === 'python-sdk' && (
        <>
          <TargetIntro
            icon={<Code2 />}
            title="ColabHive Python SDK"
            description={
              zh
                ? '用 ColabHiveClient 在代码里申请显卡：可以一直用同一台实例，也可以多账号并行分片跑。'
                : 'Request GPUs from code with ColabHiveClient: keep one session alive, or run sharded across accounts.'
            }
          />
          {toast && <ToastBanner toast={toast} />}
          <SyntaxCodeBlock
            language="python"
            label={<CodeLabel primary="python" secondary="ColabHiveClient" />}
            code={maskKey(pythonSdkSnippet)}
            copyText={pythonSdkSnippet}
            copyLabel={copyLabel}
            copiedLabel={copiedLabel}
          />
        </>
      )}
    </SlideOver>
  );
}

function ToastBanner({ toast }: { toast: { kind: 'ok' | 'err'; text: string } }) {
  return (
    <div
      role="status"
      data-tone={toast.kind === 'ok' ? 'emerald' : 'amber'}
      className="flex items-start gap-2 px-3 py-2.5 rounded-xl border text-[12.5px] leading-relaxed"
      style={{
        color: 'var(--tone-fg)',
        backgroundColor: 'var(--tone-soft)',
        borderColor: 'var(--tone-border)',
      }}
    >
      {toast.kind === 'ok' ? (
        <Check className="w-3.5 h-3.5 mt-[3px] shrink-0" />
      ) : (
        <AlertCircle className="w-3.5 h-3.5 mt-[3px] shrink-0" />
      )}
      <span className="min-w-0 break-words">{toast.text}</span>
    </div>
  );
}

function TargetIntro({
  icon,
  title,
  description,
}: {
  icon: React.ReactNode;
  title: string;
  description: string;
}) {
  return (
    <InsetWell>
      <div className="flex items-start gap-3">
        <span
          className="flex items-center justify-center w-8 h-8 shrink-0 rounded-[10px] border text-fg-secondary [&>svg]:w-4 [&>svg]:h-4"
          style={{
            backgroundColor: 'var(--surface-2)',
            borderColor: 'var(--border-subtle)',
            boxShadow: 'var(--shadow-control)',
          }}
        >
          {icon}
        </span>
        <div className="min-w-0">
          <h3 className="text-[14px] font-semibold text-fg">{title}</h3>
          <p className="mt-0.5 text-[12.5px] leading-relaxed text-fg-secondary">{description}</p>
        </div>
      </div>
    </InsetWell>
  );
}
