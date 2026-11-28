import { useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Alert, Button, Card, Descriptions, Form, Input, Space } from 'antd';
import { browserNavigationURL } from './BrowserSession.rules';
import {
  BrowserNavigationError,
  createBrowserSession,
  getBrowserSession,
  navigateBrowserSession,
  projectMembers,
  type BrowserNavigation,
} from './client';

function storageKey(accountID: string, projectID: string) {
  return `cloudlet.browser-session.${accountID}.${projectID}`;
}

function readStoredSessionID(accountID: string, projectID: string) {
  try {
    return (
      window.sessionStorage.getItem(storageKey(accountID, projectID)) ??
      undefined
    );
  } catch {
    return undefined;
  }
}

function writeStoredSessionID(
  accountID: string,
  projectID: string,
  sessionID?: string,
) {
  try {
    const key = storageKey(accountID, projectID);
    if (sessionID) window.sessionStorage.setItem(key, sessionID);
    else window.sessionStorage.removeItem(key);
  } catch {
    // Ignore storage unavailability in restricted browser contexts.
  }
}

export function BrowserSession({
  accountID,
  projectID,
}: {
  accountID: string;
  projectID: string;
}) {
  const contextKey = `${accountID}:${projectID}`;
  const [pageState, setPageState] = useState<{
    contextKey: string;
    data: BrowserNavigation;
  }>();
  const [sessionIDByContext, setSessionIDByContext] = useState<
    Record<string, string | undefined>
  >({});

  const storedSessionID = Object.prototype.hasOwnProperty.call(
    sessionIDByContext,
    contextKey,
  )
    ? sessionIDByContext[contextKey]
    : readStoredSessionID(accountID, projectID);

  const setStoredSessionID = (sessionID?: string) => {
    writeStoredSessionID(accountID, projectID, sessionID);
    setSessionIDByContext((prev) => ({ ...prev, [contextKey]: sessionID }));
  };

  const members = useQuery({
    queryKey: ['project-members', accountID, projectID],
    queryFn: () => projectMembers(projectID),
  });
  const role = members.data?.find(
    (member) => member.user_id === accountID,
  )?.role;
  const canOperate = role === 'owner' || role === 'developer';

  const restored = useQuery({
    queryKey: ['browser-session', accountID, projectID, storedSessionID],
    queryFn: async () => {
      try {
        const current = await getBrowserSession(projectID, storedSessionID!);
        if (current.status !== 'ready') {
          writeStoredSessionID(accountID, projectID, undefined);
          return null;
        }
        return current;
      } catch (err) {
        if (
          err instanceof BrowserNavigationError &&
          (err.status === 404 || err.status === 409 || err.status === 410)
        ) {
          writeStoredSessionID(accountID, projectID, undefined);
          return null;
        }
        throw err;
      }
    },
    enabled: canOperate && Boolean(storedSessionID),
    retry: false,
  });

  const create = useMutation({
    mutationFn: () => createBrowserSession(projectID),
    onSuccess: (created) => {
      setPageState(undefined);
      setStoredSessionID(created.id);
    },
  });
  const navigate = useMutation({
    mutationFn: ({ sessionID, url }: { sessionID: string; url: string }) =>
      navigateBrowserSession(projectID, sessionID, url),
    onSuccess: (data) => setPageState({ contextKey, data }),
    onError: (error) => {
      if (
        error instanceof BrowserNavigationError &&
        (error.status === 404 || error.status === 409)
      ) {
        create.reset();
        setStoredSessionID(undefined);
        setPageState(undefined);
      }
    },
  });

  if (!canOperate) return null;

  const session =
    create.data && create.data.id === storedSessionID
      ? create.data
      : (restored.data ?? undefined);
  const page =
    pageState && pageState.contextKey === contextKey
      ? pageState.data
      : undefined;
  const error = create.error ?? navigate.error ?? restored.error;
  const checkingStored =
    Boolean(storedSessionID) && restored.isPending && !create.data;

  return (
    <Card title="浏览器会话">
      <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
        {!session ? (
          <Button
            type="primary"
            loading={create.isPending || checkingStored}
            onClick={() => {
              if (create.isPending || checkingStored) return;
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
              aria-label="导航"
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
