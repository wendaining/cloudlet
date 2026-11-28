import { describe, expect, test } from 'vitest';
import {
  canManageProjectMembers,
  canRemoveProjectMember,
  currentMemberRole,
  memberPermissionDescription,
} from './ProjectMembers.rules';
import type { ProjectMember } from './client';

const members: ProjectMember[] = [
  { user_id: 'u-owner', email: 'owner@cloudlet.local', role: 'owner' },
  { user_id: 'u-dev', email: 'dev@cloudlet.local', role: 'developer' },
  { user_id: 'u-viewer', email: 'viewer@cloudlet.local', role: 'viewer' },
];

describe('project member permissions and removal rules', () => {
  test('resolves current member role for known and unknown accounts', () => {
    expect(currentMemberRole(members, 'u-owner')).toBe('owner');
    expect(currentMemberRole(members, 'u-dev')).toBe('developer');
    expect(currentMemberRole(members, 'u-viewer')).toBe('viewer');
    expect(currentMemberRole(members, 'u-other')).toBeUndefined();
    expect(currentMemberRole(undefined, 'u-owner')).toBeUndefined();
    expect(currentMemberRole(members, undefined)).toBeUndefined();
  });

  test('only project owner can manage members', () => {
    expect(canManageProjectMembers(members, 'u-owner')).toBe(true);
    expect(canManageProjectMembers(members, 'u-dev')).toBe(false);
    expect(canManageProjectMembers(members, 'u-viewer')).toBe(false);
    expect(canManageProjectMembers(members, 'u-other')).toBe(false);
  });

  test('only non-owner members can be removed by an owner', () => {
    expect(canRemoveProjectMember(members[0], true)).toBe(false);
    expect(canRemoveProjectMember(members[1], true)).toBe(true);
    expect(canRemoveProjectMember(members[2], true)).toBe(true);
    expect(canRemoveProjectMember(members[1], false)).toBe(false);
    expect(canRemoveProjectMember(members[2], false)).toBe(false);
  });

  test('returns permission description for each role', () => {
    expect(memberPermissionDescription('owner')).toContain('所有者');
    expect(memberPermissionDescription('developer')).toContain('只读');
    expect(memberPermissionDescription('viewer')).toContain('只读');
    expect(memberPermissionDescription(undefined)).toBeNull();
  });
});
