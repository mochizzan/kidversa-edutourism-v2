/**
 * Gallery index `/sessions/:sid/gallery` (F-10 / FL-10): a directory of the
 * session's children, scoped by one group filter (default: all groups).
 *
 * This page deliberately fetches no photos and opens no camera: the gallery
 * itself lives on the dedicated child page
 * (`/sessions/:sid/gallery/children/:cid`), which keeps the consent gate and
 * the capture workflow next to the photos they belong to.
 *
 * Legacy deep links from the old picker (`?gid=&cid=&stid=`) redirect to the
 * child route; `stid` rides along so an old topic link still lands on the right
 * topic.
 */
import { useEffect, useMemo, useState } from 'react'
import { Alert, Select } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { ArrowRightOutlined } from '@ant-design/icons'
import { Link, Navigate, useNavigate, useParams, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import FilterBar, { FilterField } from '../../components/FilterBar'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import RefreshButton from '../../components/RefreshButton'
import { errorCode } from '../../lib/api'
import { useCrumbName } from '../../lib/crumbs'
import type { SessionMember } from '../../lib/session-contracts'
import { ConsentTag } from '../sessions/controls'
import { useSessionDetail } from '../sessions/shared'
import './gallery.css'

interface GalleryChild extends SessionMember {
  gid: number
  group_name: string
}

/** Sentinel for the one filter: every own group in the session. */
const ALL_GROUPS = 'ALL'

export default function GalleryPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { sid } = useParams()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { detail, error, deleted, reload } = useSessionDetail(sid)

  // The breadcrumb bar shows the session's name instead of "Detail sesi".
  useCrumbName('sid', detail?.session.name)

  const [gid, setGid] = useState<number | typeof ALL_GROUPS>(ALL_GROUPS)

  useEffect(() => {
    if (deleted) navigate('/sessions', { replace: true })
  }, [deleted, navigate])

  // Own groups only: FAS foreign groups arrive reduced (no members), so they
  // must never appear as an empty row in this directory.
  const groups = useMemo(
    () => detail?.groups.filter((group) => group.own && group.members !== undefined) ?? [],
    [detail],
  )

  const children = useMemo<GalleryChild[]>(
    () =>
      groups
        .flatMap((group) =>
          (group.members ?? []).map((member) => ({
            ...member,
            gid: group.gid,
            group_name: group.name,
          })),
        )
        .sort(
          (a, b) =>
            a.group_name.localeCompare(b.group_name, 'id') ||
            a.name.localeCompare(b.name, 'id'),
        ),
    [groups],
  )

  const visible = gid === ALL_GROUPS ? children : children.filter((child) => child.gid === gid)

  // Old gallery links carried the child (and topic) in the query string.
  const legacyCid = Number(searchParams.get('cid')) || null
  if (legacyCid !== null) {
    const legacyStid = Number(searchParams.get('stid')) || null
    const suffix = legacyStid === null ? '' : `?stid=${legacyStid}`
    return <Navigate to={`/sessions/${sid}/gallery/children/${legacyCid}${suffix}`} replace />
  }

  if (error) {
    return (
      <>
        <PageHeader title={t('gallery.title')} />
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(error)}`)}
          action={<Link to="/sessions">{tc('actions.back')}</Link>}
        />
      </>
    )
  }

  if (!detail) {
    return <PageSkeleton />
  }

  const childPath = (childId: number) => `/sessions/${sid}/gallery/children/${childId}`

  const columns: ColumnsType<GalleryChild> = [
    {
      title: t('gallery.child'),
      dataIndex: 'name',
      width: 220,
      ellipsis: true,
      render: (name: string, row) => (
        <Link className="kv-gallery-child-link" to={childPath(row.child_id)} title={name}>
          {name}
        </Link>
      ),
    },
    { title: t('gallery.group'), dataIndex: 'group_name', width: 190, ellipsis: true },
    { title: t('gallery.school'), dataIndex: 'school', ellipsis: true },
    {
      title: t('consent.statusLabel'),
      key: 'consent',
      width: 170,
      render: (_, row) => <ConsentTag status={row.consent ?? ''} />,
    },
    {
      title: tc('columns.actions'),
      key: 'actions',
      width: 126,
      fixed: 'right',
      render: (_, row) => (
        <Link className="kv-gallery-open" to={childPath(row.child_id)}>
          {t('gallery.open')}
          <ArrowRightOutlined aria-hidden="true" />
        </Link>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={t('gallery.title')}
        description={`${detail.session.name} · ${detail.session.date}`}
        actions={<RefreshButton onRefresh={reload} />}
      />

      {children.length > 0 ? (
        <FilterBar
          activeCount={gid === ALL_GROUPS ? 0 : 1}
          onClear={() => setGid(ALL_GROUPS)}
          hint={t('gallery.indexHint')}
        >
          <FilterField label={t('gallery.group')} htmlFor="gallery-group" width={240}>
            <Select
              id="gallery-group"
              value={gid}
              onChange={setGid}
              options={[
                { value: ALL_GROUPS, label: t('gallery.allGroups') },
                ...groups.map((group) => ({ value: group.gid, label: group.name })),
              ]}
              aria-label={t('gallery.group')}
            />
          </FilterField>
        </FilterBar>
      ) : null}

      <DataTable<GalleryChild>
        rowKey="child_id"
        columns={columns}
        dataSource={visible}
        pagination={false}
        scroll={{ x: 880 }}
        onRow={(row) => ({
          className: 'kv-gallery-row',
          onClick: (event) => {
            // The name link and the row action already navigate; a click on
            // either must not count twice.
            if ((event.target as HTMLElement).closest('a, button')) return
            navigate(childPath(row.child_id))
          },
        })}
        empty={
          children.length === 0 ? (
            <EmptyState
              title={t('gallery.noChildrenTitle')}
              description={t('gallery.noChildrenDesc')}
            />
          ) : (
            <EmptyState title={t('gallery.groupEmpty')} />
          )
        }
      />
    </>
  )
}
