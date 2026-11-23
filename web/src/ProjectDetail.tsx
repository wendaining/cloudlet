import { Alert, Button, Card, Descriptions, Result } from 'antd';
import { Link, useParams } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { project, session } from './client';
import { ProjectMembers } from './ProjectMembers';
import styles from './app.module.css';
import { OperationList } from './OperationList';

export function ProjectDetail() {
  const { projectID } = useParams<{ projectID: string }>();
  const query = useQuery({
    queryKey: ['project', projectID],
    queryFn: () => {
      if (!projectID) throw new Error('项目标识缺失');
      return project(projectID);
    },
    enabled: Boolean(projectID),
  });
  const account = useQuery({ queryKey: ['session'], queryFn: session });
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
  return (
    <main className={styles.detail}>
      <OperationList projectID={projectID} />
      <Card
        title={query.data.name}
        extra={<Link to="/projects">返回项目列表</Link>}
      >
        <Descriptions column={1}>
          <Descriptions.Item label="项目标识">
            {query.data.id}
          </Descriptions.Item>
          <Descriptions.Item label="项目名称">
            {query.data.name}
          </Descriptions.Item>
        </Descriptions>
      </Card>
      <ProjectMembers
        projectID={query.data.id}
        accountID={account.data?.user.id}
      />
    </main>
  );
}
