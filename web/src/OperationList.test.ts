import { describe, expect, test } from 'vitest';
import {
  DEFAULT_PAGE,
  DEFAULT_PAGE_SIZE,
  applyOperationFilters,
  clampOperationPage,
  filterOperations,
  hasActiveOperationFilters,
  parseOperationFilters,
} from './OperationList.filters';
import type { Operation } from './client';

const sampleOperations: Operation[] = [
  {
    id: 'op-1',
    action: 'deploy_a',
    phase: 'received',
    outcome: 'in_progress',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
  },
  {
    id: 'op-2',
    action: 'deploy_b',
    phase: 'preparing',
    outcome: 'succeeded',
    created_at: '2026-01-01T00:01:00Z',
    updated_at: '2026-01-01T00:01:00Z',
  },
  {
    id: 'op-3',
    action: 'deploy_c',
    phase: 'preparing',
    outcome: 'failed',
    created_at: '2026-01-01T00:02:00Z',
    updated_at: '2026-01-01T00:02:00Z',
  },
];

describe('parseOperationFilters', () => {
  test('returns defaults when search params are empty', () => {
    expect(parseOperationFilters(new URLSearchParams())).toEqual({
      phase: undefined,
      outcome: undefined,
      page: DEFAULT_PAGE,
      pageSize: DEFAULT_PAGE_SIZE,
    });
  });

  test('parses valid phase, outcome, page, and pageSize', () => {
    const params = new URLSearchParams(
      'phase=preparing&outcome=failed&page=3&pageSize=20',
    );
    expect(parseOperationFilters(params)).toEqual({
      phase: 'preparing',
      outcome: 'failed',
      page: 3,
      pageSize: 20,
    });
  });

  test('ignores invalid phase, outcome, page, and unsupported pageSize', () => {
    const params = new URLSearchParams(
      'phase=unknown&outcome=cancelled&page=0&pageSize=15',
    );
    expect(parseOperationFilters(params)).toEqual({
      phase: undefined,
      outcome: undefined,
      page: DEFAULT_PAGE,
      pageSize: DEFAULT_PAGE_SIZE,
    });
  });

  test('rejects non-numeric page values', () => {
    const params = new URLSearchParams('page=2abc&pageSize=-10');
    expect(parseOperationFilters(params)).toEqual({
      phase: undefined,
      outcome: undefined,
      page: DEFAULT_PAGE,
      pageSize: DEFAULT_PAGE_SIZE,
    });
  });
});

describe('applyOperationFilters', () => {
  test('writes active filters and preserves unrelated params', () => {
    const current = new URLSearchParams('tab=ops&phase=received');
    const next = applyOperationFilters(current, {
      phase: 'activating',
      outcome: 'succeeded',
      page: 2,
      pageSize: 50,
    });
    expect(next.get('tab')).toBe('ops');
    expect(next.get('phase')).toBe('activating');
    expect(next.get('outcome')).toBe('succeeded');
    expect(next.get('page')).toBe('2');
    expect(next.get('pageSize')).toBe('50');
  });

  test('removes default and cleared params', () => {
    const current = new URLSearchParams(
      'tab=ops&phase=preparing&outcome=failed&page=3&pageSize=20',
    );
    const next = applyOperationFilters(current, {
      phase: undefined,
      outcome: undefined,
      page: 1,
      pageSize: 10,
    });
    expect(next.toString()).toBe('tab=ops');
  });
});

describe('filterOperations', () => {
  test('returns all operations when no filter is active', () => {
    expect(filterOperations(sampleOperations, {})).toHaveLength(3);
  });

  test('filters by phase only', () => {
    expect(
      filterOperations(sampleOperations, { phase: 'preparing' }).map(
        (op) => op.id,
      ),
    ).toEqual(['op-2', 'op-3']);
  });

  test('filters by outcome only', () => {
    expect(
      filterOperations(sampleOperations, { outcome: 'in_progress' }).map(
        (op) => op.id,
      ),
    ).toEqual(['op-1']);
  });

  test('filters by phase and outcome together', () => {
    expect(
      filterOperations(sampleOperations, {
        phase: 'preparing',
        outcome: 'failed',
      }).map((op) => op.id),
    ).toEqual(['op-3']);
  });
});

describe('clampOperationPage and hasActiveOperationFilters', () => {
  test('clamps out-of-range page numbers', () => {
    expect(clampOperationPage(5, 25, 10)).toBe(3);
    expect(clampOperationPage(0, 25, 10)).toBe(1);
    expect(clampOperationPage(3, 0, 10)).toBe(1);
  });

  test('reports whether any filter is active', () => {
    expect(hasActiveOperationFilters({})).toBe(false);
    expect(hasActiveOperationFilters({ phase: 'saving' })).toBe(true);
    expect(hasActiveOperationFilters({ outcome: 'succeeded' })).toBe(true);
  });
});
