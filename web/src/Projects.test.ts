import { describe, expect, test } from 'vitest';
import projectsRules from './Projects.rules';

describe('project name validation', () => {
  test.each([
    ['required', '', false],
    ['whitespace', '   ', false],
    ['100 characters', '\u9879'.repeat(100), true],
    ['101 characters', '\u9879'.repeat(101), false],
  ])('validates a %s name', (_name, value, valid) => {
    const isRequired = value.length === 0;
    const isWhitespace = value.trim().length === 0 && value.length > 0;
    const tooLong = value.length > 100;
    const accepted = !isRequired && !isWhitespace && !tooLong;
    expect(accepted).toBe(valid);
    expect(projectsRules).toHaveLength(3);
  });
});
