import { Alert, Button, Card, Descriptions, Space, Typography } from 'antd';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { environment, logout, version } from './client';
import type { User } from './client';
import styles from './app.module.css';

export function Overview({ user }: { user: User }) {
  const navigate = useNavigate();
  const cache = useQueryClient();
  const currentEnvironment = useQuery({
    queryKey: ['environment'],
    queryFn: environment,
  });
  const build = useQuery({ queryKey: ['version'], queryFn: version });
  const signOut = useMutation({
    mutationFn: logout,
    onSuccess: () => {
      cache.removeQueries({ queryKey: ['environment'] });
      cache.setQueryData(['session'], null);
    },
  });
  const failure = currentEnvironment.error ?? build.error ?? signOut.error;
  return (
    <main className={styles.overview}>
      <header className={styles.header}>
        <div>
          <Typography.Text type="secondary">Cloudlet</Typography.Text>
          <Typography.Title level={1}>环境概览</Typography.Title>
        </div>
        <Space wrap>
          <Typography.Text>{user.email}</Typography.Text>
          <Button
            aria-label="退出登录"
            aria-busy={signOut.isPending}
            onClick={() => signOut.mutate()}
            loading={signOut.isPending}
          >
            退出登录
          </Button>
        </Space>
      </header>
      <Space>
        <Button type="primary" onClick={() => navigate('/projects')}>
          进入项目控制台
        </Button>
      </Space>
      {failure ? (
        <Alert
          type="error"
          title={failure.message}
          role="alert"
          className={styles.notice}
        />
      ) : null}
      <Card
        title="运行状态"
        loading={currentEnvironment.isPending || build.isPending}
      >
        <Descriptions
          column={1}
          items={[
            {
              key: 'instance',
              label: '实例',
              children: currentEnvironment.data?.instance,
            },
            {
              key: 'database',
              label: '数据库版本',
              children: currentEnvironment.data?.database_version,
            },
            {
              key: 'version',
              label: '构建版本',
              children: build.data?.version,
            },
            { key: 'commit', label: '提交标识', children: build.data?.commit },
          ]}
        />
      </Card>
    </main>
  );
}
