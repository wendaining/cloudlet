import type { FormRule } from 'antd';
import type { ProjectMember } from './client';

export function isProjectOwner(
  members?: readonly ProjectMember[],
  accountID?: string,
): boolean {
  return Boolean(
    accountID &&
    members?.some(
      (member) => member.user_id === accountID && member.role === 'owner',
    ),
  );
}

export async function validateProjectName(
  _rule: unknown,
  value: unknown,
): Promise<void> {
  const name = typeof value === 'string' ? value : '';
  if (name.length === 0) throw new Error('请输入项目名称');
  if (name.trim().length === 0) throw new Error('项目名称不能是空白');
  if (Array.from(name).length > 100) throw new Error('项目名称最多 100 个字符');
}

export default [{ validator: validateProjectName }] as FormRule[];
