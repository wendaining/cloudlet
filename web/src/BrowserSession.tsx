import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Alert, Button, Card, Descriptions, Form, Input, Space } from 'antd';
import { browserNavigationURL } from './BrowserSession.rules';
import {
  BrowserNavigationError,
  createBrowserSession,
  navigateBrowserSession,
  projectMembers,
  type BrowserNavigation,
} from './client';

export function BrowserSession({
  accountID,
  projectID,
}: {
  accountID: string;
  projectID: string;
}) {
  const [page, setPage] = useState<BrowserNavigation>();
  const members = useQuery({
    queryKey: ['project-members', accountID, projectID],
    queryFn: () => projectMembers(projectID),
  });
  const create = useMutation({
    mutationFn: () => createBrowserSession(projectID),
    onSuccess: () => setPage(undefined),
  });
  const navigate = useMutation({
    mutationFn: ({ sessionID, url }: { sessionID: string; url: string }) =>
      navigateBrowserSession(projectID, sessionID, url),
    onSuccess: setPage,
    onError: (error) => {
      if (
        error instanceof BrowserNavigationError &&
        (error.status === 404 || error.status === 409)
      ) {
        create.reset();
        setPage(undefined);
      }
    },
  });
  const role = members.data?.find(
    (member) => member.user_id === accountID,
  )?.role;
  if (role !== 'owner' && role !== 'developer') return null;

  const session = create.data;
  const error = create.error ?? navigate.error;
  return (
    <Card title="浏览器会话">
      <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
        {!session ? (
          <Button
            type="primary"
            loading={create.isPending}
            onClick={() => {
              if (create.isPending) return;
              navigate.reset();
              create.mutate();
            }}
          >
            创建浏览器会话
          </Button>
        ) : (
          <Form<{ url: string }>
            layout="vertical"
            disabled={navigate.isPending}
            onFinish={({ url }) => {
              if (!navigate.isPending) {
                navigate.mutate({
                  sessionID: session.id,
                  url: browserNavigationURL(url),
                });
              }
            }}
          >
            <Form.Item
              label="导航地址"
              name="url"
              rules={[
                {
                  validator: async (_, value: unknown) => {
                    browserNavigationURL(value);
                  },
                },
              ]}
            >
              <Input placeholder="https://example.com" />
            </Form.Item>
            <Button
              type="primary"
              htmlType="submit"
              loading={navigate.isPending}
            >
              导航
            </Button>
          </Form>
        )}
        {error ? (
          <Alert type="error" title={error.message} role="alert" />
        ) : null}
        {page ? (
          <Descriptions column={1}>
            <Descriptions.Item label="页面标题">
              {page.title || '无标题'}
            </Descriptions.Item>
            <Descriptions.Item label="页面地址">
              <span style={{ overflowWrap: 'anywhere' }}>{page.url}</span>
            </Descriptions.Item>
          </Descriptions>
        ) : null}
      </Space>
    </Card>
  );
}
