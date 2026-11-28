import { useInfiniteQuery, useQueries } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Card,
  Empty,
  Space,
  Table,
  Tag,
  Typography,
} from 'antd';
import type { TableProps } from 'antd';
import { projectSites, staticSitePublication } from './client';
import {
  buildStaticSiteRows,
  resolveSitePublicationState,
  type StaticSiteRow,
} from './StaticSiteList.rules';
import styles from './app.module.css';

function timestamp(value: string) {
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'medium',
  }).format(new Date(value));
}

type Props = {
  projectID: string;
  accountID?: string;
  sessionPending: boolean;
  sessionError: Error | null;
};

export function StaticSiteList({
  projectID,
  accountID,
  sessionPending,
  sessionError,
}: Props) {
  const sitesQuery = useInfiniteQuery({
    queryKey: ['static-sites', accountID, projectID],
    queryFn: ({ pageParam }) => projectSites(projectID, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    enabled: Boolean(accountID),
  });

  const sites = sitesQuery.data?.pages.flatMap((page) => page.sites) ?? [];

  const publicationQueries = useQueries({
    queries: sites.map((site) => ({
      queryKey: ['static-site-publication', accountID, projectID, site.name],
      queryFn: () => staticSitePublication(projectID, site.name),
      enabled: Boolean(accountID),
    })),
  });

  const rows = buildStaticSiteRows(
    sites,
    publicationQueries.map((query) =>
      resolveSitePublicationState({
        isPending: query.isPending,
        error: query.error,
        data: query.data,
      }),
    ),
  );

  const columns: TableProps<StaticSiteRow>['columns'] = [
    {
      title: '站点名称',
      dataIndex: 'name',
      key: 'name',
      render: (name: string) => (
        <Typography.Text strong>{name}</Typography.Text>
      ),
    },
    {
      title: '活动版本',
      key: 'version',
      render: (_, row) => {
        switch (row.publication.kind) {
          case 'loading':
            return (
              <Typography.Text type="secondary">加载中...</Typography.Text>
            );
          case 'unpublished':
            return <Tag>未发布</Tag>;
          case 'error':
            return (
              <Space orientation="vertical" size={4}>
                <Tag color="error">查询失败</Tag>
                <Typography.Text type="danger" role="alert">
                  {row.publication.message}
                </Typography.Text>
              </Space>
            );
          case 'published':
            return (
              <Space size="small" wrap>
                <Tag color="success">已发布</Tag>
                <code>{row.publication.versionId}</code>
              </Space>
            );
        }
      },
    },
    {
      title: '公开域名',
      key: 'hostname',
      render: (_, row) =>
        row.publication.kind === 'published' ? (
          <code>{row.publication.hostname}</code>
        ) : (
          '—'
        ),
    },
    {
      title: '站点链接',
      key: 'url',
      render: (_, row) => {
        if (row.publication.kind !== 'published') return '—';
        if (!row.publication.url) {
          return (
            <Typography.Text type="secondary">链接格式无效</Typography.Text>
          );
        }
        return (
          <a
            href={row.publication.url}
            target="_blank"
            rel="noopener noreferrer"
          >
            {row.publication.url}
          </a>
        );
      },
    },
    {
      title: '创建时间',
      dataIndex: 'createdAt',
      key: 'createdAt',
      render: timestamp,
    },
  ];

  if (sessionPending) {
    return <Card title="静态站点" loading className={styles.section} />;
  }
  if (sessionError || !accountID) {
    return (
      <Card title="静态站点" className={styles.section}>
        <Alert
          role="alert"
          type="error"
          title={sessionError?.message ?? '登录状态已失效，请重新登录'}
        />
      </Card>
    );
  }

  return (
    <Card
      title="静态站点"
      loading={sitesQuery.isPending}
      className={styles.section}
    >
      {sitesQuery.error ? (
        <Alert
          role="alert"
          type="error"
          title={sitesQuery.error.message}
          action={
            <Button onClick={() => void sitesQuery.refetch()}>重新加载</Button>
          }
        />
      ) : rows.length > 0 ? (
        <>
          <Table<StaticSiteRow>
            columns={columns}
            dataSource={rows}
            rowKey="id"
            pagination={false}
            scroll={{ x: true }}
          />
          {sitesQuery.hasNextPage ? (
            <div className={styles.loadMore}>
              <Button
                loading={sitesQuery.isFetchingNextPage}
                onClick={() => void sitesQuery.fetchNextPage()}
              >
                加载更多站点
              </Button>
            </div>
          ) : null}
        </>
      ) : (
        <Empty description="暂无静态站点" />
      )}
    </Card>
  );
}
