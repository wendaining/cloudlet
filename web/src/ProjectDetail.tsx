import { useState } from 'react';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  Result,
  Space,
} from 'antd';
import { Link, useParams } from 'react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  project,
  projectMembers,
  renameProject,
  session,
  type Project,
} from './client';
import projectsRules, { isProjectOwner } from './Projects.rules';
import { ProjectMembers } from './ProjectMembers';
import styles from './app.module.css';
import { BrowserSession } from './BrowserSession';
import { OperationList } from './OperationList';
import { StaticSiteList } from './StaticSiteList';

type RenameProjectValues = {
  name: string;
};

type RenameVariables = {
  accountID: string;
  projectID: string;
  name: string;
};

export function ProjectDetail() {
  const { projectID } = useParams<{ projectID: string }>();
  const cache = useQueryClient();
  const [form] = Form.useForm<RenameProjectValues>();
  const [editingContext, setEditingContext] = useState<{
    accountID?: string;
    projectID: string;
  } | null>(null);

  const query = useQuery({
    queryKey: ['project', projectID],
    queryFn: () => {
      if (!projectID) throw new Error('项目标识缺失');
      return project(projectID);
    },
    enabled: Boolean(projectID),
  });
  const account = useQuery({ queryKey: ['session'], queryFn: session });
  const accountID = account.data?.user.id;
  const members = useQuery({
    queryKey: ['project-members', accountID, projectID],
    queryFn: () => {
      if (!projectID) throw new Error('项目标识缺失');
      return projectMembers(projectID);
    },
    enabled: Boolean(accountID && projectID),
  });

  const rename = useMutation({
    mutationKey: ['project-rename', accountID, projectID],
    mutationFn: ({ projectID: targetID, name }: RenameVariables) =>
      renameProject(targetID, { name }),
    onSuccess: async (updated) => {
      cache.setQueryData<Project>(['project', updated.id], updated);
      cache.setQueryData<Project[]>(['projects'], (current) =>
        current?.map((item) => (item.id === updated.id ? updated : item)),
      );
      await Promise.all([
        cache.invalidateQueries({ queryKey: ['project', updated.id] }),
        cache.invalidateQueries({ queryKey: ['projects'] }),
      ]);
      setEditingContext(null);
    },
  });

  if (!projectID) return <Result status="404" title="页面不存在" />;
  if (query.isPending)
    return (
      <main className={styles.detail}>
        <Card loading />
      </main>
    );
  if (query.error)
    return (
      <main className={styles.detail}>
        <Alert
          type="error"
          title={query.error.message}
          action={
            <Button onClick={() => void query.refetch()}>重新加载</Button>
          }
        />
      </main>
    );
  if (!query.data)
    return (
      <main className={styles.detail}>
        <Result
          status="404"
          title="项目不存在或没有访问权限"
          extra={<Link to="/projects">返回项目列表</Link>}
        />
      </main>
    );

  const currentProject = query.data;
  const canManage = isProjectOwner(members.data, accountID);
  const editing =
    editingContext?.accountID === accountID &&
    editingContext?.projectID === projectID;
  const renameBelongsToContext =
    rename.variables?.accountID === accountID &&
    rename.variables?.projectID === projectID;

  const startEditing = () => {
    rename.reset();
    form.setFieldsValue({ name: currentProject.name });
    setEditingContext({ accountID, projectID: currentProject.id });
  };

  const cancelEditing = () => {
    rename.reset();
    setEditingContext(null);
  };

  return (
    <main className={styles.detail}>
      <OperationList
        projectID={projectID}
        accountID={accountID}
        sessionPending={account.isPending}
        sessionError={account.error}
      />
      <Card
        title={currentProject.name}
        extra={<Link to="/projects">返回项目列表</Link>}
        className={styles.section}
      >
        <Descriptions column={1}>
          <Descriptions.Item label="项目标识">
            {currentProject.id}
          </Descriptions.Item>
          <Descriptions.Item label="项目名称">
            {currentProject.name}
          </Descriptions.Item>
        </Descriptions>
        {canManage ? (
          editing ? (
            <Form<RenameProjectValues>
              form={form}
              layout="vertical"
              className={styles.renameForm}
              initialValues={{ name: currentProject.name }}
              onFinish={(values) => {
                if (!accountID) return;
                rename.mutate({
                  accountID,
                  projectID: currentProject.id,
                  name: values.name,
                });
              }}
            >
              <Form.Item name="name" label="项目名称" rules={projectsRules}>
                <Input
                  aria-label="项目名称"
                  showCount={{
                    formatter: ({ value }) =>
                      `${Array.from(value).length} / 100`,
                  }}
                />
              </Form.Item>
              {renameBelongsToContext && rename.isError ? (
                <Alert
                  type="error"
                  title={rename.error.message}
                  role="alert"
                  className={styles.notice}
                />
              ) : null}
              <Space>
                <Button
                  aria-label="保存名称"
                  aria-busy={renameBelongsToContext && rename.isPending}
                  type="primary"
                  htmlType="submit"
                  loading={renameBelongsToContext && rename.isPending}
                >
                  保存名称
                </Button>
                <Button
                  onClick={cancelEditing}
                  disabled={renameBelongsToContext && rename.isPending}
                >
                  取消
                </Button>
              </Space>
            </Form>
          ) : (
            <div className={styles.renameActions}>
              <Button onClick={startEditing}>编辑名称</Button>
            </div>
          )
        ) : null}
        {renameBelongsToContext && rename.isSuccess && !editing ? (
          <Alert
            type="success"
            title="项目名称已更新"
            role="status"
            className={styles.notice}
          />
        ) : null}
      </Card>
      {account.data ? (
        <BrowserSession
          key={account.data.user.id + ':' + projectID}
          accountID={account.data.user.id}
          projectID={projectID}
        />
      ) : null}
      <StaticSiteList
        projectID={query.data.id}
        accountID={account.data?.user.id}
        sessionPending={account.isPending}
        sessionError={account.error}
      />
      <ProjectMembers
        projectID={query.data.id}
        accountID={account.data?.user.id}
      />
    </main>
  );
}
