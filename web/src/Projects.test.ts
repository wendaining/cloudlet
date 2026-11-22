import { describe, expect, test } from 'vitest';
import { validateProjectName } from './Projects.rules';

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
