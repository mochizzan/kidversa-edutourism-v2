/**
 * AI narrative editor (F-11 / FL-12): the empty state, the generate →
 * PATCH contract with trim + toast, the ≤2000 cap and counter, AI_FAILED
 * keeping the old text inline, NOTHING_TO_NARRATE as a warning, and the
 * generic save-error alert.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import NarrativePanel from './NarrativePanel'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'
import { expectNotification } from '../../test/toast'

const P = '/api/sessions/1/topics/3/children/2'

const SAVED = {
  text: 'Aisyah mengeksplorasi batik bersama kelompoknya.',
  model: 'gemini-2.5-flash',
  generated_at: '2026-10-09T10:00:00Z',
}

describe('narrative panel', () => {
  it('shows the empty state until a narrative exists', async () => {
    server.use(http.get(`${P}/narrative`, () => ok(null)))
    renderApp(<NarrativePanel prefix={P} />)

    await screen.findByText('Belum ada narasi.')
    screen.getByRole('button', { name: 'Buat narasi' })
    const area = screen.getByRole('textbox')
    expect(area).toHaveAttribute('maxlength', '2000')
    expect(screen.getByText('0/2000')).toBeInTheDocument()
  })

  it('generates via POST and updates text, model line and counter', async () => {
    server.use(
      http.get(`${P}/narrative`, () => ok(null)),
      http.post(`${P}/narrative`, () => ok(SAVED)),
    )
    const user = userEvent.setup()
    renderApp(<NarrativePanel prefix={P} />)

    await screen.findByText('Belum ada narasi.')
    // The antd loading icon lingers in the accessible name briefly — match loosely.
    await user.click(screen.getByRole('button', { name: /Buat narasi/ }))

    const area = screen.getByRole('textbox') as HTMLTextAreaElement
    await waitFor(() => expect(area.value).toBe(SAVED.text))
    expect(screen.getByText(`${SAVED.text.length}/2000`)).toBeInTheDocument()
    // Model line renders once settled (narrative && !dirty).
    screen.getByText('gemini-2.5-flash · 2026-10-09T10:00:00Z')
    // Save is disabled without edits.
    expect(screen.getByRole('button', { name: /Simpan narasi/ })).toBeDisabled()
    // Generation is a heavy action → a result card, not a toast.
    await expectNotification(
      'Narasi siap',
      'Tinjau dan ubah sebelum dikirim ke orang tua.',
    )
  })

  it('AI_FAILED keeps the old text and shows the inline warning', async () => {
    server.use(
      http.get(`${P}/narrative`, () => ok(SAVED)),
      http.post(`${P}/narrative`, () =>
        HttpResponse.json({ error: { code: 'AI_FAILED', message: 'model down' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<NarrativePanel prefix={P} />)

    const area = screen.getByRole('textbox') as HTMLTextAreaElement
    await waitFor(() => expect(area.value).toBe(SAVED.text))
    await user.click(screen.getByRole('button', { name: /Buat ulang/ }))

    await screen.findByText('AI gagal membuat narasi. Coba lagi.')
    // The previous narrative survived.
    expect(area.value).toBe(SAVED.text)
  })

  it('saves a dirty edit via PATCH with the trimmed text and toasts', async () => {
    let patched: unknown = null
    server.use(
      http.get(`${P}/narrative`, () => ok(SAVED)),
      http.patch(`${P}/narrative`, async ({ request }) => {
        patched = await request.json()
        return ok({ ...SAVED, text: 'Diedit manual.' })
      }),
    )
    const user = userEvent.setup()
    renderApp(<NarrativePanel prefix={P} />)

    const area = screen.getByRole('textbox') as HTMLTextAreaElement
    await waitFor(() => expect(area.value).toBe(SAVED.text))
    await user.clear(area)
    await user.type(area, '  Diedit manual.  ')

    const save = screen.getByRole('button', { name: /Simpan narasi/ })
    await waitFor(() => expect(save).toBeEnabled())
    await user.click(save)

    await screen.findByText('Narasi tersimpan.')
    await waitFor(() => expect(patched).toEqual({ text: 'Diedit manual.' }))
    // PATCH success re-syncs the editor from the response: the whitespace-padded
    // input settles to the stored text, so the counter follows and the save
    // button goes clean (disabled) instead of staying dirty.
    await waitFor(() => expect(area.value).toBe('Diedit manual.'))
    expect(screen.getByText('14/2000')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Simpan narasi/ })).toBeDisabled()
  })

  it('NOTHING_TO_NARRATE on save renders the inline warning', async () => {
    server.use(
      http.get(`${P}/narrative`, () => ok(SAVED)),
      http.patch(`${P}/narrative`, () =>
        HttpResponse.json({ error: { code: 'NOTHING_TO_NARRATE', message: 'empty' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<NarrativePanel prefix={P} />)

    const area = screen.getByRole('textbox') as HTMLTextAreaElement
    await waitFor(() => expect(area.value).toBe(SAVED.text))
    await user.type(area, ' lagi')
    await user.click(screen.getByRole('button', { name: /Simpan narasi/ }))

    await screen.findByText('Belum ada penilaian untuk dinarasi.')
    // Warning, not a hard error: the textarea stays editable.
    expect(area).toBeEnabled()
  })

  it('a generic save failure renders the error alert inline', async () => {
    server.use(
      http.get(`${P}/narrative`, () => ok(SAVED)),
      http.patch(`${P}/narrative`, () =>
        HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<NarrativePanel prefix={P} />)

    const area = screen.getByRole('textbox') as HTMLTextAreaElement
    await waitFor(() => expect(area.value).toBe(SAVED.text))
    await user.type(area, ' lagi')
    await user.click(screen.getByRole('button', { name: /Simpan narasi/ }))

    const card = screen.getByText('Narasi AI').closest('.ant-card') as HTMLElement
    await within(card).findByText('Layanan sedang sibuk. Coba lagi.')
  })
})
