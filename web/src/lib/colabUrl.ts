import type { Account, RuntimeSession } from '../types';

export interface ColabWebAccessCheck {
  allowed: boolean;
  reason?: 'missing_session' | 'contributor' | 'non_colab';
}

/**
 * Matches a runtime session to its owning pooled account record.
 */
export function findSessionAccount(
  session: RuntimeSession | null | undefined,
  accounts: Account[],
): Account | undefined {
  if (!session || accounts.length === 0) return undefined;
  if (session.account_id) {
    const byId = accounts.find((a) => a.id === session.account_id);
    if (byId) return byId;
  }
  const email = (session.account_email || '').trim().toLowerCase();
  if (email) {
    return accounts.find((a) => (a.email || '').trim().toLowerCase() === email);
  }
  return undefined;
}

/**
 * Determines whether opening the external Google Colab web URL is meaningful for a session.
 */
export function canOpenColabWebForSession(
  session: RuntimeSession | null | undefined,
  account: Account | undefined,
): ColabWebAccessCheck {
  if (!session) {
    return { allowed: false, reason: 'missing_session' };
  }
  if (account?.provider && String(account.provider) !== 'colab') {
    return { allowed: false, reason: 'non_colab' };
  }
  if (account?.is_contributor) {
    return { allowed: false, reason: 'contributor' };
  }
  return { allowed: true };
}

/**
 * Appends Google's `authuser=<email>` selector to a Colab attach URL when an unmasked
 * account email is known, so multi-account Chrome browsers switch straight to the right
 * Google profile instead of falling back to `authuser=0` and failing to attach.
 */
export function buildSmartColabWebUrl(
  rawUrl: string | undefined,
  accountEmail?: string,
): string {
  const fallback = 'https://colab.research.google.com/';
  const base = (rawUrl || '').trim() || fallback;
  const email = (accountEmail || '').trim();
  if (!email || !email.includes('@') || email.includes('*')) {
    return base;
  }
  try {
    const parsed = new URL(base);
    if (parsed.hostname.endsWith('colab.research.google.com')) {
      parsed.searchParams.set('authuser', email);
      return parsed.toString();
    }
    return base;
  } catch {
    return base;
  }
}
