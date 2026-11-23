import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
  Button,
  Card,
  Descriptions,
  Drawer,
  Empty,
  Table,
  Tag,
} from 'antd';
import type { TableProps } from 'antd';
import { projectOperations } from './client';
import type { Operation } from './client';

const phaseLabels: Record<string, string> = {
  received: '已接收',
  saving: '保存中',
  preflight: '预检中',
  preparing: '准备中',
  activating: '激活中',
  finalizing: '收尾中',
};
const outcomeLabels: Record<string, string> = {
  in_progress: '进行中',
  succeeded: '成功',
  failed: '失败',
};
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
export function OperationList({
  projectID,
  accountID,
  sessionPending,
  sessionError,
}: Props) {
  const [selected, setSelected] = useState<Operation | null>(null);
  const query = useQuery({
    queryKey: ['operations', accountID, projectID],
    queryFn: () => projectOperations(projectID),
    enabled: Boolean(accountID),
  });
  const columns: TableProps<Operation>['columns'] = [
    {
      title: '操作标识',
      dataIndex: 'id',
      key: 'id',
      render: (id: string) => <code>{id}</code>,
    },
    { title: '操作', dataIndex: 'action', key: 'action' },
    {
      title: '阶段',
      dataIndex: 'phase',
      key: 'phase',
      render: (phase: string) => phaseLabels[phase] ?? phase,
    },
    {
      title: '结果',
      dataIndex: 'outcome',
      key: 'outcome',
      render: (outcome: string) => (
        <Tag
          color={
            outcome === 'failed'
              ? 'error'
              : outcome === 'succeeded'
                ? 'success'
                : 'processing'
          }
        >
          {outcomeLabels[outcome] ?? outcome}
        </Tag>
      ),
    },
    {
      title: '创建时间',
      dataIndex: 'created_at',
      key: 'created_at',
      render: timestamp,
    },
    {
      title: '更新时间',
      dataIndex: 'updated_at',
      key: 'updated_at',
      render: timestamp,
    },
    {
      title: '详情',
      key: 'detail',
      render: (_, operation) => (
        <Button type="link" onClick={() => setSelected(operation)}>
          查看详情
        </Button>
      ),
    },
  ];

  if (sessionPending) return <Card title="操作记录" loading />;
  if (sessionError || !accountID) {
    return (
      <Card title="操作记录">
        <Alert
          role="alert"
          type="error"
          title={sessionError?.message ?? '登录状态已失效，请重新登录'}
        />
      </Card>
    );
  }

  return (
    <Card title="操作记录" loading={query.isPending}>
      {query.error ? (
        <Alert
          role="alert"
          type="error"
          title={query.error.message}
          action={
            <Button onClick={() => void query.refetch()}>重新加载</Button>
          }
        />
      ) : query.data?.length ? (
        <Table<Operation>
          columns={columns}
          dataSource={query.data}
          rowKey="id"
          pagination={false}
          scroll={{ x: true }}
        />
      ) : (
        <Empty description="暂无操作记录" />
      )}
      <Drawer
        title="操作详情"
        open={Boolean(selected)}
        onClose={() => setSelected(null)}
        footer={<Button onClick={() => setSelected(null)}>返回操作列表</Button>}
      >
        {selected ? (
          <Descriptions column={1}>
            <Descriptions.Item label="操作标识">
              <code>{selected.id}</code>
            </Descriptions.Item>
            <Descriptions.Item label="操作">
              {selected.action}
            </Descriptions.Item>
            <Descriptions.Item label="阶段">
              {phaseLabels[selected.phase] ?? selected.phase}
            </Descriptions.Item>
            <Descriptions.Item label="结果">
              {outcomeLabels[selected.outcome] ?? selected.outcome}
            </Descriptions.Item>
            <Descriptions.Item label="创建时间">
              {timestamp(selected.created_at)}
            </Descriptions.Item>
            <Descriptions.Item label="更新时间">
              {timestamp(selected.updated_at)}
            </Descriptions.Item>
          </Descriptions>
        ) : null}
      </Drawer>
    </Card>
  );
}
