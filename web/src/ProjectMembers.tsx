import { Alert, Button, Card, Form, Input, Select, Table, Tag } from 'antd';
import type { TableProps } from 'antd';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  projectMembers,
  upsertProjectMember,
  type ProjectMember,
  type ProjectMemberRole,
} from './client';
import styles from './app.module.css';

type MemberFormValues = {
  email: string;
  role: ProjectMemberRole;
};

type SaveVariables = {
  accountID: string;
  values: MemberFormValues;
};

const roleLabels: Record<ProjectMember['role'], string> = {
  owner: '所有者',
  developer: '开发者',
  viewer: '观察者',
};

export function ProjectMembers({
  projectID,
  accountID,
}: {
  projectID: string;
  accountID?: string;
}) {
  const cache = useQueryClient();
  const members = useQuery({
    queryKey: ['project-members', accountID, projectID],
    queryFn: () => projectMembers(projectID),
    enabled: Boolean(accountID),
  });
  const save = useMutation({
    mutationKey: ['project-member-save', accountID, projectID],
    mutationFn: ({ values }: SaveVariables) =>
      upsertProjectMember(projectID, values),
    onSuccess: async (_member, { accountID: savedBy }) => {
      await cache.invalidateQueries({
        queryKey: ['project-members', savedBy, projectID],
      });
    },
  });
  const canManage = Boolean(
    accountID &&
    members.data?.some(
      (member) => member.user_id === accountID && member.role === 'owner',
    ),
  );
  const saveBelongsToAccount = save.variables?.accountID === accountID;

  const columns: TableProps<ProjectMember>['columns'] = [
    { title: '邮箱', dataIndex: 'email', key: 'email' },
    {
      title: '角色',
      dataIndex: 'role',
      key: 'role',
      render: (role: ProjectMember['role']) => (
        <Tag
          color={
            role === 'owner'
              ? 'gold'
              : role === 'developer'
                ? 'blue'
                : 'default'
          }
        >
          {roleLabels[role]}
        </Tag>
      ),
    },
  ];

  return (
    <Card
      title="项目成员"
      loading={members.isPending}
      className={styles.members}
    >
      {members.error ? (
        <Alert
          type="error"
          title={members.error.message}
          role="alert"
          action={
            <Button onClick={() => void members.refetch()}>重新加载</Button>
          }
        />
      ) : members.data ? (
        <Table<ProjectMember>
          columns={columns}
          dataSource={members.data}
          rowKey={(member) => member.user_id}
          pagination={false}
        />
      ) : null}
      {canManage ? (
        <Form<MemberFormValues>
          className={styles.memberForm}
          layout="inline"
          initialValues={{ role: 'developer' }}
          onFinish={(values) => {
            if (accountID) save.mutate({ accountID, values });
          }}
        >
          <Form.Item
            name="email"
            rules={[
              { required: true, message: '请输入成员邮箱' },
              { type: 'email', message: '请输入有效邮箱' },
            ]}
          >
            <Input aria-label="成员邮箱" placeholder="成员邮箱" />
          </Form.Item>
          <Form.Item name="role" rules={[{ required: true }]}>
            <Select
              aria-label="成员角色"
              options={[
                { value: 'developer', label: '开发者' },
                { value: 'viewer', label: '观察者' },
              ]}
            />
          </Form.Item>
          <Form.Item>
            <Button
              aria-label="保存成员"
              aria-busy={saveBelongsToAccount && save.isPending}
              type="primary"
              htmlType="submit"
              loading={saveBelongsToAccount && save.isPending}
            >
              保存成员
            </Button>
          </Form.Item>
        </Form>
      ) : null}
      {saveBelongsToAccount && save.isSuccess ? (
        <Alert
          type="success"
          title={`${save.data.email} 的角色已保存`}
          role="status"
          className={styles.notice}
        />
      ) : null}
      {saveBelongsToAccount && save.isError ? (
        <Alert
          type="error"
          title={save.error.message}
          role="alert"
          className={styles.notice}
        />
      ) : null}
    </Card>
  );
}
