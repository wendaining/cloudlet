import { describe, expect, test } from 'vitest';
import { isProjectOwner, validateProjectName } from './Projects.rules';

describe('project name validation', () => {
  test('rejects a missing name', async () => {
    await expect(validateProjectName(undefined, '')).rejects.toThrow(
      '请输入项目名称',
    );
  });

  test('rejects whitespace', async () => {
    await expect(validateProjectName(undefined, '   ')).rejects.toThrow(
      '项目名称不能是空白',
    );
  });

  test('accepts 100 characters', async () => {
    await expect(
      validateProjectName(undefined, '项'.repeat(100)),
    ).resolves.toBeUndefined();
  });

  test('rejects 101 characters', async () => {
    await expect(
      validateProjectName(undefined, '项'.repeat(101)),
    ).rejects.toThrow('项目名称最多 100 个字符');
  });
});

describe('project owner permission', () => {
  const members = [
    { user_id: 'u1', email: 'owner@cloudlet.local', role: 'owner' as const },
    { user_id: 'u2', email: 'dev@cloudlet.local', role: 'developer' as const },
    { user_id: 'u3', email: 'viewer@cloudlet.local', role: 'viewer' as const },
  ];

  test('returns true for project owner', () => {
    expect(isProjectOwner(members, 'u1')).toBe(true);
  });

  test('returns false for developer and viewer', () => {
    expect(isProjectOwner(members, 'u2')).toBe(false);
    expect(isProjectOwner(members, 'u3')).toBe(false);
  });

  test('returns false when account or members are missing', () => {
    expect(isProjectOwner(members, undefined)).toBe(false);
    expect(isProjectOwner(undefined, 'u1')).toBe(false);
    expect(isProjectOwner(members, 'u999')).toBe(false);
  });
});
