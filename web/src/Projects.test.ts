import { describe, expect, test } from 'vitest';
import projectsRules from './Projects.rules';

describe('project name validation', () => {
  const [required, whitespace, maxLength] = projectsRules;

  test('requires a name', () => {
    expect(required).toEqual(
      expect.objectContaining({ required: true, message: '请输入项目名称' }),
    );
  });
  test('rejects whitespace', () => {
    expect(whitespace).toEqual(
      expect.objectContaining({
        whitespace: true,
        message: '项目名称不能是空白',
      }),
    );
  });
  test('rejects more than 100 characters', () => {
    expect(maxLength).toEqual(
      expect.objectContaining({ max: 100, message: '项目名称最多 100 个字符' }),
    );
  });
  test('accepts a 100-character name', () => {
    expect('项'.repeat(100).length).toBeLessThanOrEqual(100);
  });
});
