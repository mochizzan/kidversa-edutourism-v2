/**
 * Session reports matrix + batch WA send (F-12 / FL-13).
 *
 * Matrix: `GET /api/sessions/:sid/reports` (full array — the ops matrix is
 * not paged), refetched on every event that can change a cell
 * (`report-link/assessment/narrative/mission-pick/photo.changed`). Group
 * filter (REQ7): `useSessionDetail` powers the Select — choosing a group
 * narrows the matrix to that group's members (`child_id` membership).
 *
 * Batch: pick rows → client chunks ≤10 per request → sequential `POST
 * /api/sessions/:sid/batch-send` (server jitters 3–10 s between sends); each
 * response merges per-item `{session_topic_id, child_id, status, code?}` into
 * the results list (SENT/FAILED + translated code). Retry re-sends only the
 * FAILED rows; an item set that never got a per-item response (whole-chunk
 * envelope failure / crash) replays under its ORIGINAL idempotency key (06§3
 * same-key replay — the key is held per item set, sessionStorage-persisted
 * for crash survival), while never-posted item sets mint fresh keys.
 * WA_FAILED rows expose the copy-URL fallback straight from the matrix.
 * REPORT_INCOMPLETE arrives per item and never blocks the other items.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, App as AntApp, Button, Card, Flex, Select, Space, Table, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { CopyOutlined, SendOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useFeedback } from '../../lib/feedback'
import { useEventRefetch } from '../../lib/list'
import { RowAction, RowActions } from '../../components/RowActions'
import StatusPill from '../../components/StatusPill'
import { useSessionDetail } from '../sessions/shared'
import { copyText, type ReportsRow } from '../../lib/session-contracts'

const CHUNK_SIZE = 10

interface ItemResult {
  status: 'SENT' | 'FAILED'
  code?: string
}

interface BatchItem {
  child_id: number
  session_topic_id: number
}

function resultKey(item: BatchItem): string {
  return `${item.child_id}:${item.session_topic_id}`
}

/** sessionStorage prefix for the pending per-chunk idem map (06§3 crash survival). */
const IDEM_STORE_PREFIX = 'kv-batch-idem:'

/** Persist the pending (never-completed) chunk keys; an unusable store degrades to fresh keys. */
function persistIdemMap(sid: string, map: Map<string, string>): void {
  try {
    sessionStorage.setItem(IDEM_STORE_PREFIX + sid, JSON.stringify([...map]))
  } catch {
    // Storage unavailable/full → the next run mints fresh keys (old behavior).
  }
}

export default function BatchSendPanel({ sid }: { sid: string }) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const feedback = useFeedback()
  const [rows, setRows] = useState<ReportsRow[] | null>(null)
  const [error, setError] = useState<unknown>(null)
  /**
   * Deterministic settle flag for the Table's `loading` prop: set exactly
   * once the request completes (success OR failure), so the skeleton never
   * outlives the request. `rows === null` below stays the sentinel for "no
   * successful fetch yet" (it drives the pre-card error alert).
   */
  const [settled, setSettled] = useState(false)
  const [selectedKeys, setSelectedKeys] = useState<string[]>([])
  /** REQ7: the group filter — `null` shows every row. */
  const [groupId, setGroupId] = useState<number | null>(null)
  const { detail } = useSessionDetail(sid)
  const [results, setResults] = useState<Record<string, ItemResult>>({})
  const [busy, setBusy] = useState(false)
  const [chunkError, setChunkError] = useState<unknown>(null)
  /** Pending per-chunk idem keys (item sets with NO per-item response yet). */
  const idemMapRef = useRef<Map<string, string> | null>(null)

  const load = useCallback(async () => {
    try {
      const data = await api.get<ReportsRow[]>(`/api/sessions/${sid}/reports`)
      // Normalize empty/`null` payloads to [] — the skeleton unmounts and the
      // Table renders its standard empty state instead of loading forever.
      setRows(Array.isArray(data) ? data : [])
      setError(null)
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setSettled(true)
    }
  }, [sid])

  useEffect(() => {
    setRows(null)
    setSettled(false)
    void load()
  }, [load])

  // 06§3: pending chunk keys survive a reload (crash survival); a corrupt or
  // missing store starts empty → fresh keys (old behavior).
  useEffect(() => {
    try {
      const raw = sessionStorage.getItem(IDEM_STORE_PREFIX + sid)
      const entries: [string, string][] = raw === null ? [] : JSON.parse(raw)
      idemMapRef.current = new Map(entries)
    } catch {
      idemMapRef.current = new Map()
    }
  }, [sid])

  useEventRefetch(
    ['report-link.changed', 'assessment.changed', 'narrative.changed', 'mission-pick.changed', 'photo.changed'],
    load,
  )

  const rowKey = (row: ReportsRow) => `${row.child_id}:${row.session_topic_id}`

  const groups = detail?.groups ?? []
  const selectedGroup = groups.find((group) => group.gid === groupId)
  /** FAS reduced foreign groups expose no members, so selecting one keeps all rows. */
  const memberIds = selectedGroup?.members
    ? new Set(selectedGroup.members.map((member) => member.child_id))
    : null
  /** The matrix rows the panel operates on: group filter + raw matrix. */
  const displayedRows = memberIds ? (rows ?? []).filter((row) => memberIds.has(row.child_id)) : (rows ?? [])

  const sendItems = async (items: BatchItem[]) => {
    setBusy(true)
    setChunkError(null)
    let sent = 0
    let failed = 0
    try {
      for (let index = 0; index < items.length; index += CHUNK_SIZE) {
        const chunk = items.slice(index, index + CHUNK_SIZE)
        // 06§3 same-key replay: an item set that never got a per-item response
        // (whole-chunk failure / crash) re-posts under its ORIGINAL key — the
        // server replays or re-executes instead of double-sending; only NEW
        // item sets mint fresh keys. Identity = the set's sorted items.
        const canonical = chunk.map(resultKey).sort().join('|')
        const idemMap = idemMapRef.current ?? (idemMapRef.current = new Map())
        let idemKey = idemMap.get(canonical)
        if (idemKey === undefined) {
          idemKey = crypto.randomUUID()
          idemMap.set(canonical, idemKey)
          persistIdemMap(sid, idemMap)
        }
        try {
          const response = await api.post<{ items: (BatchItem & ItemResult)[] }>(
            `/api/sessions/${sid}/batch-send`,
            { items: chunk },
            { idempotencyKey: idemKey },
          )
          // Per-item response → the key is consumed server-side; drop it so a
          // later send of the same set is a fresh, intentional run.
          idemMap.delete(canonical)
          persistIdemMap(sid, idemMap)
          // Count OUTSIDE the state updater: React may run an updater twice in
          // StrictMode, which would double the totals in the result card.
          for (const item of response.items) {
            if (item.status === 'SENT') sent += 1
            else failed += 1
          }
          setResults((current) => {
            const next = { ...current }
            for (const item of response.items) {
              next[resultKey(item)] = { status: item.status, code: item.code }
            }
            return next
          })
        } catch (failure: unknown) {
          // Whole-chunk envelope failure (RATE_LIMITED / INTERNAL / …): mark the
          // chunk items FAILED and stop — later chunks would fail the same way.
          // The set's key STAYS stored: "Ulangi yang gagal" replays it under the
          // same key (no per-item response was received for these items).
          const code = errorCode(failure)
          failed += chunk.length
          setResults((current) => {
            const next = { ...current }
            for (const item of chunk) next[resultKey(item)] = { status: 'FAILED', code }
            return next
          })
          setChunkError(failure)
          break
        }
      }
    } finally {
      setBusy(false)
      if (sent + failed > 0) {
        feedback.heavy(
          t('reports.batchDoneTitle'),
          t('reports.batchDone', { sent, failed }),
        )
      }
    }
  }

  const selectedRows = displayedRows.filter((row) => selectedKeys.includes(rowKey(row)))
  const failedRows = displayedRows.filter((row) => results[rowKey(row)]?.status === 'FAILED')
  const resultRows = displayedRows.filter((row) => results[rowKey(row)] !== undefined)
  const sentCount = resultRows.filter((row) => results[rowKey(row)].status === 'SENT').length
  const failedCount = resultRows.length - sentCount

  const copyUrl = (url: string) => {
    void copyText(url).then((ok) =>
      ok ? message.success(tc('actions.copied')) : message.error(tc('errors.INTERNAL')),
    )
  }

  const matrixColumns: ColumnsType<ReportsRow> = [
    { title: t('reports.colChild'), dataIndex: 'child_name' },
    { title: t('reports.colTopic'), dataIndex: 'topic_name' },
    {
      title: t('reports.colComplete'),
      dataIndex: 'complete',
      width: 100,
      render: (value: boolean) =>
        value ? (
          <StatusPill tone="positive">{t('readiness.complete')}</StatusPill>
        ) : (
          <StatusPill>{t('reports.no')}</StatusPill>
        ),
    },
    {
      title: t('reports.colNarrative'),
      dataIndex: 'narrative',
      width: 90,
      render: (value: boolean) => (value ? t('reports.yes') : t('reports.no')),
    },
    { title: t('reports.colMissions'), dataIndex: 'missions', width: 80, align: 'right' },
    {
      title: t('reports.colPhoto'),
      dataIndex: 'candidate_photo',
      width: 90,
      render: (value: boolean | null) => (value === null ? '—' : value ? t('reports.yes') : t('reports.no')),
    },
    {
      title: t('reports.colLinks'),
      key: 'links',
      width: 140,
      render: (_, row) => (
        <Space size={2}>
          {row.report_url ? <StatusPill tone="info">R</StatusPill> : null}
          {row.gallery_url ? <StatusPill>G</StatusPill> : null}
          {!row.report_url && !row.gallery_url ? '—' : null}
        </Space>
      ),
    },
  ]

  const resultColumns: ColumnsType<ReportsRow> = [
    { title: t('reports.colChild'), dataIndex: 'child_name' },
    { title: t('reports.colTopic'), dataIndex: 'topic_name' },
    {
      title: t('list.status'),
      key: 'status',
      width: 320,
      render: (_, row) => {
        const result = results[rowKey(row)]
        if (!result) return null
        if (result.status === 'SENT') {
          return <StatusPill tone="positive">{t('reports.sent')}</StatusPill>
        }
        return (
          <Space size={4} wrap>
            <StatusPill tone="attention">{tc(`errors.${result.code ?? 'WA_FAILED'}`)}</StatusPill>
            <RowActions>
              {row.report_url ? (
                <RowAction
                  label={t('reports.copyReportUrl')}
                  icon={<CopyOutlined />}
                  onClick={() => copyUrl(row.report_url ?? '')}
                />
              ) : null}
              {row.gallery_url ? (
                <RowAction
                  label={t('reports.copyGalleryUrl')}
                  icon={<CopyOutlined />}
                  onClick={() => copyUrl(row.gallery_url ?? '')}
                />
              ) : null}
            </RowActions>
          </Space>
        )
      },
    },
  ]

  if (error && rows === null) {
    return (
      <Alert
        type="error"
        showIcon
        message={tc(`errors.${errorCode(error)}`)}
        action={<Button onClick={load}>{tc('actions.retry')}</Button>}
      />
    )
  }

  return (
    <Card style={{ marginBottom: 16 }}>
      <Flex justify="space-between" align="flex-start" gap="middle" wrap style={{ marginBottom: 8 }}>
        <div>
          <Typography.Title level={2} style={{ margin: 0 }}>
            {t('reports.batchTitle')}
          </Typography.Title>
          <Typography.Text type="secondary">{t('reports.batchHint')}</Typography.Text>
        </div>
        <Space wrap>
          <Typography.Text type="secondary">
            {selectedKeys.length > 0
              ? t('reports.batchSelected', { count: selectedKeys.length })
              : t('reports.batchEmpty')}
          </Typography.Text>
          <Button
            type="primary"
            icon={<SendOutlined />}
            loading={busy}
            disabled={selectedKeys.length === 0}
            onClick={() => void sendItems(selectedRows)}
          >
            {busy ? t('reports.batchSending') : t('reports.batchSend')}
          </Button>
          <Button
            loading={busy}
            disabled={failedRows.length === 0}
            onClick={() => void sendItems(failedRows)}
          >
            {t('reports.batchRetry')}
          </Button>
          <Select
            placeholder={t('reports.groupFilter')}
            aria-label={t('reports.groupFilter')}
            value={groupId ?? undefined}
            onChange={(value) => setGroupId(value)}
            options={groups.map((group) => ({ value: group.gid, label: group.name }))}
          />
        </Space>
      </Flex>

      <Table<ReportsRow>
        rowKey={rowKey}
        size="small"
        loading={!settled}
        columns={matrixColumns}
        dataSource={displayedRows}
        locale={{ emptyText: tc('empty') }}
        pagination={false}
        scroll={{ x: 720 }}
        rowSelection={{
          type: 'checkbox',
          selectedRowKeys: selectedKeys,
          onChange: (keys) => setSelectedKeys(keys.map(String)),
          getCheckboxProps: () => ({ disabled: busy }),
        }}
      />

      {chunkError ? (
        <Alert
          style={{ marginTop: 12 }}
          type="error"
          showIcon
          message={tc(
            `errors.${errorCode(chunkError)}`,
          )}
        />
      ) : null}

      {resultRows.length > 0 ? (
        <>
          <Alert
            style={{ marginTop: 12 }}
            type={failedCount > 0 ? 'warning' : 'success'}
            showIcon
            message={t('reports.batchDone', { sent: sentCount, failed: failedCount })}
          />
          <Table<ReportsRow>
            rowKey={rowKey}
            size="small"
            style={{ marginTop: 8 }}
            columns={resultColumns}
            dataSource={resultRows}
            pagination={false}
            scroll={{ x: 640 }}
          />
        </>
      ) : null}
    </Card>
  )
}
