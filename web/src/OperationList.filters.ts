import type { Operation } from './client';

export const phaseLabels: Record<string, string> = {
  received: '已接收',
  saving: '保存中',
  preflight: '预检中',
  preparing: '准备中',
  activating: '激活中',
  finalizing: '收尾中',
};

export const outcomeLabels: Record<string, string> = {
  in_progress: '进行中',
  succeeded: '成功',
  failed: '失败',
};

export const phaseOptions = Object.entries(phaseLabels).map(
  ([value, label]) => ({
    value,
    label,
  }),
);

export const outcomeOptions = Object.entries(outcomeLabels).map(
  ([value, label]) => ({
    value,
    label,
  }),
);

export const DEFAULT_PAGE = 1;
export const DEFAULT_PAGE_SIZE = 10;
export const PAGE_SIZE_OPTIONS = [10, 20, 50];

export type OperationFilterState = {
  phase?: string;
  outcome?: string;
  page: number;
  pageSize: number;
};

function parsePositiveInt(raw: string | null): number | null {
  if (!raw || !/^\d+$/.test(raw)) return null;
  const parsed = Number(raw);
  if (!Number.isSafeInteger(parsed) || parsed < 1) return null;
  return parsed;
}

function normalizePhase(raw: string | null | undefined): string | undefined {
  if (!raw || !Object.hasOwn(phaseLabels, raw)) return undefined;
  return raw;
}

function normalizeOutcome(raw: string | null | undefined): string | undefined {
  if (!raw || !Object.hasOwn(outcomeLabels, raw)) return undefined;
  return raw;
}

export function parseOperationFilters(
  searchParams: URLSearchParams,
): OperationFilterState {
  const page = parsePositiveInt(searchParams.get('page')) ?? DEFAULT_PAGE;
  const rawPageSize = parsePositiveInt(searchParams.get('pageSize'));
  const pageSize =
    rawPageSize && PAGE_SIZE_OPTIONS.includes(rawPageSize)
      ? rawPageSize
      : DEFAULT_PAGE_SIZE;

  return {
    phase: normalizePhase(searchParams.get('phase')),
    outcome: normalizeOutcome(searchParams.get('outcome')),
    page,
    pageSize,
  };
}

export function applyOperationFilters(
  current: URLSearchParams,
  next: OperationFilterState,
): URLSearchParams {
  const updated = new URLSearchParams(current);

  const phase = normalizePhase(next.phase);
  if (phase) {
    updated.set('phase', phase);
  } else {
    updated.delete('phase');
  }

  const outcome = normalizeOutcome(next.outcome);
  if (outcome) {
    updated.set('outcome', outcome);
  } else {
    updated.delete('outcome');
  }

  if (Number.isSafeInteger(next.page) && next.page > DEFAULT_PAGE) {
    updated.set('page', String(next.page));
  } else {
    updated.delete('page');
  }

  if (
    next.pageSize !== DEFAULT_PAGE_SIZE &&
    PAGE_SIZE_OPTIONS.includes(next.pageSize)
  ) {
    updated.set('pageSize', String(next.pageSize));
  } else {
    updated.delete('pageSize');
  }

  return updated;
}

export function filterOperations(
  operations: readonly Operation[],
  filters: Pick<OperationFilterState, 'phase' | 'outcome'>,
): Operation[] {
  return operations.filter((operation) => {
    if (filters.phase && operation.phase !== filters.phase) return false;
    if (filters.outcome && operation.outcome !== filters.outcome) return false;
    return true;
  });
}

export function clampOperationPage(
  page: number,
  total: number,
  pageSize: number,
): number {
  const safePageSize = pageSize > 0 ? pageSize : DEFAULT_PAGE_SIZE;
  const totalPages = Math.max(1, Math.ceil(total / safePageSize));
  if (!Number.isSafeInteger(page) || page < 1) return DEFAULT_PAGE;
  return Math.min(page, totalPages);
}

export function hasActiveOperationFilters(
  filters: Pick<OperationFilterState, 'phase' | 'outcome'>,
): boolean {
  return Boolean(filters.phase || filters.outcome);
}
