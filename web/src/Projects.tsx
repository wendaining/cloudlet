import {
  Alert,
  Button,
  Card,
  Empty,
  Form,
  Input,
  Table,
  Typography,
} from 'antd';
import type { TableProps } from 'antd';
import { Link, useNavigate } from 'react-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { createProject, projects } from './client';
import projectsRules from './Projects.rules';
import type { Project } from './client';
import styles from './app.module.css';

type CreateProjectValues = {
  name: string;
};

export function Projects() {
  const cache = useQueryClient();
  const navigate = useNavigate();
  const list = useQuery({ queryKey: ['projects'], queryFn: projects });
  const mutation = useMutation({
    mutationFn: createProject,
    onSuccess: (created) => {
      cache.setQueryData<Project[]>(['projects'], (current = []) => [
        ...current,
        created,
      ]);
      navigate(`/projects/${created.id}`);
    },
  });

  const columns: TableProps<Project>['columns'] = [
    {
      title: '项目',
      dataIndex: 'name',
      key: 'name',
      render: (_, project) => (
        <Link to={`/projects/${project.id}`}>{project.name}</Link>
      ),
    },
  ];

  return (
    <section className={styles.projects}>
      <div className={styles.projectsHeader}>
        <Typography.Title level={2}>项目</Typography.Title>
        <Card className={styles.createCard} title="创建项目">
          <Form<CreateProjectValues>
            layout="vertical"
            onFinish={(values) => mutation.mutate(values)}
          >
            <Form.Item name="name" label="项目名称" rules={projectsRules}>
              <Input aria-label="项目名称" maxLength={100} showCount />
            </Form.Item>
            {mutation.error ? (
              <Alert type="error" title={mutation.error.message} role="alert" />
            ) : null}
            <Button
              aria-label="创建项目"
              aria-busy={mutation.isPending}
              type="primary"
              htmlType="submit"
              loading={mutation.isPending}
            >
              创建项目
            </Button>
          </Form>
        </Card>
      </div>
      <Card
        title="当前可见项目"
        loading={list.isPending}
        className={styles.listCard}
      >
        {list.error ? (
          <Alert
            type="error"
            title={list.error.message}
            role="alert"
            action={
              <Button onClick={() => void list.refetch()}>重新加载</Button>
            }
          />
        ) : list.data?.length ? (
          <Table<Project>
            columns={columns}
            dataSource={list.data}
            rowKey={(project) => project.id}
            pagination={false}
          />
        ) : (
          <Empty description="暂无可见项目" />
        )}
      </Card>
    </section>
  );
}
