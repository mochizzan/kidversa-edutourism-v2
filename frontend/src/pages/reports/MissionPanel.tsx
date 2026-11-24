/**
 * Mission picker (F-12 / FL-13): current picks `GET P/missions`, available
 * bank `GET /api/missions?topic_id=` (master topic), replace-set
 * `PUT P/missions {mission_ids}` — ≤4 client-blocked (5th checkbox disabled)
 * and `MISSION_LIMIT` still handled server-side. Modal opens lazily; SSE
 * `mission-pick.changed` refetches picks, `mission.changed` refetches the
 * OPEN bank (a closed one reloads lazily on the next open).
 */
import { useCallback, useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Card, Checkbox, Empty, Flex, List, Modal, Space, Spin, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useEventRefetch } from '../../lib/list'
import type { PickedMission } from '../../lib/session-contracts'

interface MissionRow {
  id: number
  name: string
  body: string
}

const MAX_MISSIONS = 4

export default function MissionPanel({
  prefix,
  masterTopicId,
}: {
  prefix: string
  masterTopicId: number
}) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const [picked, setPicked] = useState<PickedMission[] | null>(null)
  const [open, setOpen] = useState(false)
  const [available, setAvailable] = useState<MissionRow[] | null>(null)
  const [selected, setSelected] = useState<number[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const load = useCallback(() => {
    api.get<PickedMission[]>(`${prefix}/missions`).then(
      (data) => {
        setPicked(data)
        setError(null)
      },
      (failure: unknown) => setError(failure),
    )
  }, [prefix])

  useEffect(() => {
    setPicked(null)
    load()
  }, [load])

  useEventRefetch(['mission-pick.changed'], load)

  const loadBank = useCallback(
    (seedSelection: boolean) => {
      api.get<MissionRow[]>('/api/missions', { topic_id: masterTopicId, page: 1, per_page: 1000 }).then(
        (rows) => {
          setAvailable(rows)
          if (seedSelection) setSelected((picked ?? []).map((mission) => mission.mission_id))
        },
        (failure: unknown) => setError(failure),
      )
    },
    [masterTopicId, picked],
  )

  const openPicker = () => {
    setOpen(true)
    setAvailable(null)
    setError(null)
    loadBank(true)
  }

  // 02§2c: a bank edit elsewhere refreshes the OPEN picker only — a closed
  // one reloads lazily on the next open, and the refresh must never clobber
  // the in-progress selection (seedSelection=false).
  useEventRefetch(['mission.changed'], () => {
    if (open) loadBank(false)
  })

  const toggle = (missionId: number) => {
    setSelected((current) => {
      if (current.includes(missionId)) return current.filter((id) => id !== missionId)
      if (current.length >= MAX_MISSIONS) return current // client pre-blocks the 5th
      return [...current, missionId]
    })
  }

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      await api.put(`${prefix}/missions`, { mission_ids: selected })
      message.success(t('reports.missionsSaved'))
      setOpen(false)
      load() // own-200 refetch (+ `mission-pick.changed` for other tabs)
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  // Picked missions that no longer link the topic stay selectable (shown even
  // when absent from the bank list).
  const bankRows: MissionRow[] = [
    ...(available ?? []),
    ...(picked ?? [])
      .filter((mission) => !(available ?? []).some((row) => row.id === mission.mission_id))
      .map((mission) => ({ id: mission.mission_id, name: mission.name, body: mission.body })),
  ]

  return (
    <Card style={{ marginBottom: 16 }}>
      <Flex justify="space-between" align="flex-start" gap="middle" wrap style={{ marginBottom: 8 }}>
        <Typography.Title level={2} style={{ margin: 0 }}>
          {t('reports.missions')}
        </Typography.Title>
        <Button size="small" onClick={openPicker}>
          {t('reports.missionsEdit')}
        </Button>
      </Flex>
      {picked === null ? (
        <Spin size="small" />
      ) : picked.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('reports.missionsEmpty')} style={{ margin: '8px 0' }} />
      ) : (
        <List
          size="small"
          dataSource={picked}
          split={false}
          renderItem={(mission) => (
            <List.Item key={mission.mission_id} style={{ padding: '4px 0' }}>
              <Typography.Text style={{ fontSize: 12 }}>
                <strong>{mission.name}</strong> — {mission.body}
              </Typography.Text>
            </List.Item>
          )}
        />
      )}
      {error && !open ? (
        <Alert
          style={{ marginTop: 8 }}
          type="error"
          showIcon
          message={tc(`errors.${errorCode(error)}`)}
        />
      ) : null}

      <Modal
        open={open}
        title={t('reports.missionsTitle')}
        okText={tc('actions.save')}
        cancelText={tc('actions.cancel')}
        okButtonProps={{ disabled: selected.length > MAX_MISSIONS }}
        confirmLoading={busy}
        maskClosable={!busy}
        onOk={() => void save()}
        onCancel={() => setOpen(false)}
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Typography.Text type="secondary">
            {selected.length}/{MAX_MISSIONS}
          </Typography.Text>
          {available === null ? (
            <Spin size="small" />
          ) : (
            bankRows.map((mission) => {
              const checked = selected.includes(mission.id)
              const disabled = !checked && selected.length >= MAX_MISSIONS
              return (
                <Checkbox
                  key={mission.id}
                  checked={checked}
                  disabled={disabled}
                  onChange={() => toggle(mission.id)}
                  style={{ alignItems: 'flex-start' }}
                >
                  <strong>{mission.name}</strong>
                  <br />
                  <Typography.Text type="secondary">{mission.body}</Typography.Text>
                </Checkbox>
              )
            })
          )}
          {error ? (
            <Alert
              type="error"
              showIcon
              message={tc(`errors.${errorCode(error)}`)}
            />
          ) : null}
        </Space>
      </Modal>
    </Card>
  )
}
