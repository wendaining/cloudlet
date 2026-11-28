import type { ProjectMember } from './client';

export function currentMemberRole(
  members: ProjectMember[] | undefined,
  accountID?: string,
): ProjectMember['role'] | undefined {
  if (!accountID || !members) return undefined;
  return members.find((member) => member.user_id === accountID)?.role;
}

export function canManageProjectMembers(
  members: ProjectMember[] | undefined,
  accountID?: string,
): boolean {
  return currentMemberRole(members, accountID) === 'owner';
}

export function canRemoveProjectMember(
  member: ProjectMember,
  canManage: boolean,
): boolean {
  return canManage && member.role !== 'owner';
}

export function memberPermissionDescription(
  role: ProjectMember['role'] | undefined,
): string | null {
  switch (role) {
    case 'owner':
      return '当前权限：所有者，可添加、调整或移除非所有者成员。';
    case 'developer':
      return '当前权限：开发者（只读），仅项目所有者可以管理项目成员。';
    case 'viewer':
      return '当前权限：观察者（只读），仅项目所有者可以管理项目成员。';
    default:
      return null;
  }
}
