/**
 * Table row actions: one icon per action, one word of explanation per icon.
 *
 * The action column is a scan target, not prose. Text buttons repeated on
 * every row turn the right edge of a table into a wall of words and force a
 * wide fixed column. Here each action becomes a 26px ghost icon:
 *
 * - `aria-label` keeps the action's name for screen readers (and for the
 *   tests that previously matched the visible text), while `Tooltip` shows
 *   the same words on hover — so icon-only never means unlabelled.
 * - `accent` marks the row's primary decision (approve / activate / send) in
 *   brand magenta; everything else stays neutral ink and only turns red on
 *   hover when it is destructive, so a list of rows has one accent at most.
 * - `RowActionsDivider` separates destructive actions from the rest.
 * - `RowActionGroup` collapses secondary actions into one overflow button;
 *   clicking it opens a menu that names every action it holds.
 * - `confirm` builds the Popconfirm around the real button, so the confirm
 *   popover never depends on prop cloning through a custom component. A
 *   disabled action skips the popover; its reason is the only explanation.
 * - `disabledReason` replaces the tooltip copy while disabled and is mirrored
 *   as an sr-only description, so a gated icon still explains itself.
 */
import {
  cloneElement,
  isValidElement,
  useId,
  useState,
  type ReactElement,
  type ReactNode,
} from 'react'
import { Button, Dropdown, Popconfirm, Tooltip } from 'antd'
import type { MenuProps } from 'antd'
import { EllipsisOutlined } from '@ant-design/icons'
import './row-actions.css'

export interface RowActionProps {
  /** Accessible name and default tooltip copy: use the action's verb. */
  label: string
  icon: ReactNode
  onClick?: () => void
  /** Primary decision for this row (approve, activate, send). */
  accent?: boolean
  /** Destructive: coral on hover, plus a confirm when `confirm` is set. */
  danger?: boolean
  disabled?: boolean
  loading?: boolean
  /** Tooltip copy while disabled: says why the action is unavailable. */
  disabledReason?: ReactNode
  /** Popconfirm title; the confirm's OK calls `onClick`. */
  confirm?: ReactNode
  /** Second Popconfirm line; use it for the consequence of confirming. */
  confirmDescription?: ReactNode
}

export function RowAction({
  label,
  icon,
  onClick,
  accent = false,
  danger = false,
  disabled = false,
  loading = false,
  disabledReason,
  confirm,
  confirmDescription,
}: RowActionProps) {
  const reasonId = useId()
  // A disabled icon still needs a way to say why. The reason becomes the
  // tooltip and an sr-only description; the accessible name stays the verb.
  const reason = disabled ? disabledReason : undefined

  const button = (
    <Button
      type="text"
      size="small"
      className={`kv-row-action${accent ? ' kv-row-action--accent' : ''}`}
      aria-label={label}
      aria-describedby={reason ? reasonId : undefined}
      icon={icon}
      disabled={disabled}
      loading={loading}
      danger={danger}
      onClick={confirm ? undefined : onClick}
    />
  )

  return (
    <Tooltip title={reason ?? label} mouseEnterDelay={0.35}>
      {/* The span keeps the tooltip reachable when the button is disabled and
          keeps Popconfirm's cloned handlers on the button itself. */}
      <span className="kv-row-action__target">
        {confirm && !disabled ? (
          <Popconfirm
            title={confirm}
            description={confirmDescription}
            onConfirm={onClick}
          >
            {button}
          </Popconfirm>
        ) : (
          button
        )}
        {reason ? (
          <span id={reasonId} className="kv-sr-only">
            {reason}
          </span>
        ) : null}
      </span>
    </Tooltip>
  )
}

export function RowActions({ children }: { children: ReactNode }) {
  return <span className="kv-row-actions">{children}</span>
}

export function RowActionsDivider() {
  return <span className="kv-row-actions__divider" aria-hidden="true" />
}

export interface RowActionGroupItem {
  key: string
  label: string
  icon: ReactNode
  onClick: () => void
}

export interface RowActionGroupProps {
  /** Accessible name + tooltip for the overflow trigger. */
  label: string
  items: RowActionGroupItem[]
}

/**
 * Menu icons are decoration: the item's words are its accessible name, so a
 * screen reader must not read the icon's own label ("login") before it.
 */
function decorative(icon: ReactNode): ReactNode {
  if (!isValidElement(icon)) return icon
  return cloneElement(icon as ReactElement<{ 'aria-hidden'?: boolean }>, { 'aria-hidden': true })
}

/**
 * Secondary actions behind one overflow button. The trigger is still labelled
 * on hover; once the menu is open the tooltip steps aside so it never covers
 * the action names the menu is there to show.
 */
export function RowActionGroup({ label, items }: RowActionGroupProps) {
  const [open, setOpen] = useState(false)
  if (items.length === 0) return null

  const menuItems: MenuProps['items'] = items.map((item) => ({
    key: item.key,
    label: item.label,
    icon: decorative(item.icon),
  }))

  return (
    <Tooltip title={label} open={open ? false : undefined} mouseEnterDelay={0.35}>
      <span className="kv-row-action__target">
        <Dropdown
          trigger={['click']}
          open={open}
          onOpenChange={setOpen}
          placement="bottomRight"
          menu={{
            items: menuItems,
            onClick: ({ key }) => {
              setOpen(false)
              items.find((item) => item.key === key)?.onClick()
            },
          }}
        >
          <Button
            type="text"
            size="small"
            className="kv-row-action"
            aria-label={label}
            aria-haspopup="menu"
            icon={<EllipsisOutlined />}
          />
        </Dropdown>
      </span>
    </Tooltip>
  )
}
