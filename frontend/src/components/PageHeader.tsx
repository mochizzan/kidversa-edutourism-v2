/** Compact editorial page header: 18–20px/600 title, optional description + actions (DESIGN §type scale). */
import type { ReactNode } from 'react'
import { Flex, Space, Typography } from 'antd'

export interface PageHeaderProps {
  title: string
  description?: ReactNode
  /** Right-aligned action buttons (create, refresh, …). */
  actions?: ReactNode
}

export default function PageHeader({ title, description, actions }: PageHeaderProps) {
  return (
    <Flex className="kv-page-header" justify="space-between" align="flex-start" gap="middle" wrap>
      <div className="kv-page-header__text">
        <Typography.Title level={1} className="kv-page-title">
          {title}
        </Typography.Title>
        {description ? (
          <Typography.Text type="secondary" className="kv-page-desc">
            {description}
          </Typography.Text>
        ) : null}
      </div>
      {actions ? (
        <Space className="kv-page-actions" size={8} wrap>
          {actions}
        </Space>
      ) : null}
    </Flex>
  )
}
