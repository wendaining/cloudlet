import { useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Form,
  Input,
  Modal,
  Select,
  Table,
  Tag,
  Typography,
} from 'antd';
import type { TableProps } from 'antd';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  projectMembers,
  removeProjectMember,
  upsertProjectMember,
  type ProjectMember,
  type ProjectMemberRole,
} from './client';
import {
  canManageProjectMembers,
  canRemoveProjectMember,
  currentMemberRole,
  memberPermissionDescription,
} from './ProjectMembers.rules';
import styles from './app.module.css';

type MemberFormValues = {
  email: string;
  role: ProjectMemberRole;
};

type SaveVariables = {
  accountID: string;
  values: MemberFormValues;
};

type RemoveVariables = {
  accountID: string;
  member: ProjectMember;
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
  const [confirmTarget, setConfirmTarget] = useState<RemoveVariables | null>(
    null,
  );
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
  const remove = useMutation({
    mutationKey: ['project-member-remove', accountID, projectID],
    mutationFn: ({ member }: RemoveVariables) =>
      removeProjectMember(projectID, member.user_id),
    onSuccess: async (_data, { accountID: removedBy }) => {
      setConfirmTarget(null);
      await cache.invalidateQueries({
        queryKey: ['project-members', removedBy, projectID],
      });
    },
    onError: () => {
      setConfirmTarget(null);
    },
  });

  const role = currentMemberRole(members.data, accountID);
  const canManage = canManageProjectMembers(members.data, accountID);
  const permissionText = memberPermissionDescription(role);
  const saveBelongsToAccount = save.variables?.accountID === accountID;
  const removeBelongsToAccount = remove.variables?.accountID === accountID;
  const activeConfirmTarget =
    confirmTarget?.accountID === accountID ? confirmTarget : null;
  const removePending = removeBelongsToAccount && remove.isPending;

  const columns: TableProps<ProjectMember>['columns'] = [
    { title: '邮箱', dataIndex: 'email', key: 'email' },
    {
      title: '角色',
      dataIndex: 'role',
      key: 'role',
      render: (memberRole: ProjectMember['role']) => (
        <Tag
          color={
            memberRole === 'owner'
              ? 'gold'
              : memberRole === 'developer'
                ? 'blue'
                : 'default'
          }
        >
          {roleLabels[memberRole]}
        </Tag>
      ),
    },
    ...(canManage
      ? [
          {
            title: '操作',
            key: 'actions',
            render: (_value: unknown, member: ProjectMember) =>
              canRemoveProjectMember(member, canManage) ? (
                <Button
                  type="link"
                  danger
                  size="small"
                  aria-label={`移除 ${member.email}`}
                  disabled={removePending}
                  onClick={() => {
                    if (!accountID) return;
                    save.reset();
                    remove.reset();
                    setConfirmTarget({ accountID, member });
                  }}
                >
                  移除
                </Button>
              ) : (
                <Typography.Text type="secondary">不可移除</Typography.Text>
              ),
          },
        ]
      : []),
  ];

  return (
    <Card
      title="项目成员"
      loading={members.isPending}
      className={styles.members}
      extra={
        <Button
          aria-label="刷新成员"
          loading={members.isFetching}
          onClick={() => void members.refetch()}
        >
          刷新
        </Button>
      }
    >
      {permissionText ? (
        <Typography.Paragraph type="secondary" className={styles.notice}>
          {permissionText}
        </Typography.Paragraph>
      ) : null}
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
          scroll={{ x: 'max-content' }}
        />
      ) : null}
      {canManage ? (
        <Form<MemberFormValues>
          className={styles.memberForm}
          layout="inline"
          initialValues={{ role: 'developer' }}
          onFinish={(values) => {
            if (!accountID) return;
            remove.reset();
            save.mutate({ accountID, values });
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
      {removeBelongsToAccount && remove.isSuccess ? (
        <Alert
          type="success"
          title={`${remove.variables.member.email} 已移除`}
          role="status"
          className={styles.notice}
        />
      ) : null}
      {removeBelongsToAccount && remove.isError ? (
        <Alert
          type="error"
          title={remove.error.message}
          role="alert"
          className={styles.notice}
        />
      ) : null}
      <Modal
        open={Boolean(activeConfirmTarget)}
        title="确认移除成员"
        okText="确认移除"
        cancelText="取消"
        okButtonProps={{
          danger: true,
          loading: removePending,
        }}
        cancelButtonProps={{
          disabled: removePending,
        }}
        onCancel={() => {
          if (!removePending) setConfirmTarget(null);
        }}
        onOk={() => {
          if (activeConfirmTarget) {
            save.reset();
            remove.mutate(activeConfirmTarget);
          }
        }}
      >
        {activeConfirmTarget ? (
          <p>
            确定要移除成员 {activeConfirmTarget.member.email}（
            {roleLabels[activeConfirmTarget.member.role]}
            ）吗？移除后该账号将无法继续访问此项目。
          </p>
        ) : null}
      </Modal>
    </Card>
  );
}
