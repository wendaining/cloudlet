import { Alert, Button, Result, Spin } from 'antd';
import { useQuery } from '@tanstack/react-query';
import { Route, Routes } from 'react-router';
import { session } from './client';
import { Login } from './Login';
import { Overview } from './Overview';
import { Projects } from './Projects';
import { ProjectDetail } from './ProjectDetail';
import styles from './app.module.css';

function AuthenticatedRoutes() {
  const account = useQuery({ queryKey: ['session'], queryFn: session });
  if (account.isPending)
    return (
      <main className={styles.login}>
        <Spin description="正在连接管理服务" size="large">
          <div className={styles.loading} />
        </Spin>
      </main>
    );
  if (account.error)
    return (
      <main className={styles.login}>
        <Alert
          type="error"
          title={account.error.message}
          action={
            <Button onClick={() => void account.refetch()}>重新连接</Button>
          }
        />
      </main>
    );
  if (!account.data) return <Login />;
  return (
    <Routes>
      <Route path="/" element={<Overview session={account.data} />} />
      <Route path="/projects" element={<Projects />} />
      <Route path="/projects/:projectID" element={<ProjectDetail />} />
      <Route path="*" element={<Result status="404" title="页面不存在" />} />
    </Routes>
  );
}

export function App() {
  return <AuthenticatedRoutes />;
}
