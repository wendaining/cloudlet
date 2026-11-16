import { Alert, Button, Card, Form, Input, Typography } from 'antd';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { login } from './client';
import type { LoginRequest } from './client';
import styles from './app.module.css';

export function Login() {
  const cache = useQueryClient();
  const mutation = useMutation({
    mutationFn: login,
    onSuccess: (user) => cache.setQueryData(['session'], user),
  });
  return (
    <main className={styles.login}>
      <Card className={styles.loginCard}>
        <Typography.Title level={1}>Cloudlet</Typography.Title>
        <Typography.Paragraph type="secondary">
          登录管理平台
        </Typography.Paragraph>
        <Form<LoginRequest>
          layout="vertical"
          onFinish={(values) => mutation.mutate(values)}
        >
          <Form.Item
            name="email"
            label="邮箱"
            rules={[
              { required: true, type: 'email', message: '请输入有效邮箱' },
            ]}
          >
            <Input autoComplete="username" />
          </Form.Item>
          <Form.Item
            name="password"
            label="密码"
            rules={[{ required: true, message: '请输入密码' }]}
          >
            <Input.Password autoComplete="current-password" />
          </Form.Item>
          {mutation.error ? (
            <Alert
              type="error"
              title={mutation.error.message}
              role="alert"
              className={styles.notice}
            />
          ) : null}
          <Button
            aria-label="登录"
            aria-busy={mutation.isPending}
            type="primary"
            htmlType="submit"
            block
            loading={mutation.isPending}
          >
            登录
          </Button>
        </Form>
      </Card>
    </main>
  );
}
