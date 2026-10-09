# DESIGN.md

## Design Direction

**Product:** YowTix event-management dashboard  
**Audience:** Event managers and operators who need to scan operational performance, sales, schedules, and recent activity quickly.  
**Primary job:** Turn a dense set of event metrics into a calm, highly scannable command center where the manager can understand *what is happening, what changed, and what needs attention* within a few seconds.

### Visual thesis

The reference uses a **quiet editorial dashboard** rather than a conventional SaaS admin template. The canvas is almost entirely white, cards are separated by hairline borders instead of heavy shadows, and a restrained olive data color carries most of the visual hierarchy. Coral is reserved for change/attention states, while muted violet identifies contextual actions and selected navigation.

The overall feeling should be:

- clean, premium, calm, and operational
- information-dense without looking crowded
- rounded but not playful
- light-weight borders instead of elevated cards
- compact typography with strong numerical emphasis
- restrained color usage so the data itself remains the hero

Do not introduce gradients, glassmorphism, oversized hero sections, excessive illustrations, or generic “AI dashboard” decoration.

---

## Pass 1 — Compact Token System

### Color

| Token | Hex | Usage |
|---|---|---|
| `canvas` | `#FFFFFF` | Main page background |
| `surface-subtle` | `#FCFCFB` | Very light secondary surfaces / chart backgrounds |
| `border` | `#E8E8E4` | Card outlines, dividers, field separators |
| `text-primary` | `#20201E` | Main headings, metrics, important labels |
| `text-secondary` | `#777873` | Body labels, metadata, supporting values |
| `olive` | `#B7C88A` | Primary data visualization color, positive/neutral emphasis |
| `olive-deep` | `#ACBB71` | Stronger chart bars / active data marks |
| `coral` | `#EA7137` | Warnings, negative change, attention states |
| `coral-soft` | `#F3B89C` | Calendar / secondary coral states |
| `violet` | `#5D5B78` | Selected navigation, contextual pills, secondary emphasis |

### Color rules

1. **Olive is the dashboard signature.** Use it for the majority of charts and positive/neutral KPI emphasis.
2. **Coral means attention or movement.** Reserve it for negative deltas, alerts, or event/category differentiation.
3. **Violet is structural/contextual.** Use it for selected navigation, secondary tabs, or small category markers.
4. Keep most UI surfaces white. Color should occupy a small portion of the total visual area.
5. Never use color alone to communicate status; pair color with text, icons, or numeric change.

---

### Typography

Use a compact modern sans-serif system with a slightly humanist feel.

**Recommended roles:**

- **Display / major headings:** `Manrope`, 600–700. Used sparingly for page title and high-value metric emphasis.
- **UI / body:** `Inter`, 400–600. Used for navigation, card titles, labels, descriptions, controls, and metadata.
- **Data / utility numbers:** `Inter`, 600–700 with tabular numerals enabled (`font-variant-numeric: tabular-nums`).

If the implementation already has a typography system, preserve its existing font stack and reproduce the **weight, density, and letter-spacing** characteristics rather than adding a new dependency only for visual imitation.

### Type scale

| Role | Size | Weight | Approx. line-height |
|---|---:|---:|---:|
| Page title | 18–20px | 600 | 24px |
| KPI value | 20–24px | 700 | 28px |
| Card title | 11–13px | 600 | 16px |
| Body / row text | 10–12px | 500 | 16px |
| Metadata | 8–10px | 400–500 | 14px |
| Tiny chart labels | 7–9px | 500 | 12px |

The screenshot is intentionally compact. Do not scale text up simply to “improve readability” if doing so breaks the density and proportions of the reference.

---

### Shape

- Card radius: **8–10px**
- Small control radius: **6–8px**
- Pills / tags: **full radius**
- Borders: **1px solid**, low contrast
- Shadows: **none or extremely subtle**
- Avoid large rounded containers and excessive nested cards.

The reference achieves separation primarily through borders, spacing, and background contrast—not through drop shadows.

---

### Spacing

Use an **8px base rhythm**, with 4px sub-steps where necessary.

Common values:

`4 / 8 / 12 / 16 / 20 / 24 / 32`

The screenshot favors compact internal spacing, especially inside KPI and activity cards.

---

## Layout Concept

### Global shell

A two-level shell:

1. **Top navigation bar** — brand at left, primary navigation in the center-left, utility actions and user profile at right.
2. **Dashboard workspace** — dense card grid below the header.

The dashboard should feel like one continuous workspace, not a collection of unrelated pages.

### Reference wireframe

```text
┌──────────────────────────────────────────────────────────────────────────────┐
│ BRAND      [Dashboard] [Events] [Tickets] [Analytics] [Support]   ○ ⚙  USER │
├──────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│ ┌───────────┐ ┌───────────┐ ┌───────────┐ ┌────────────────┐ ┌────────────┐ │
│ │ KPI        │ │ KPI       │ │ KPI       │ │ Recent         │ │ Ticket     │ │
│ │ Total      │ │ Ticket    │ │ Upcoming  │ │ Activities     │ │ Sales      │ │
│ │ Events     │ │ Sold      │ │ Events    │ │ + timeline     │ │ Summary    │ │
│ └───────────┘ └───────────┘ └───────────┘ └────────────────┘ │ + ranking  │ │
│                                                                              │
│ ┌──────────────────────────────────┐ ┌──────────────────────┐ │            │ │
│ │ Revenue Breakdown                │ │ Recent activity       │ │            │ │
│ │                                  │ │ / event feed          │ │            │ │
│ │ chart + compact summary          │ │                      │ │            │ │
│ └──────────────────────────────────┘ └──────────────────────┘ │            │ │
│                                                                              │
│ ┌──────────────────────────────────┐ ┌──────────────────────┐ │            │ │
│ │ Event list + map                 │ │ Event Engagement      │ │            │ │
│ │                                  │ │ monthly calendar      │ │            │ │
│ └──────────────────────────────────┘ └──────────────────────┘ │            │ │
│                                                                              │
│                                         ┌──────────────────────────────────┐ │
│                                         │ Ticket Sales Overtime            │ │
│                                         │ vertical bar series + legend     │ │
│                                         │ KPI rows                         │ │
│                                         └──────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Grid behavior

The desktop reference is approximately a **12-column mental grid** with an intentionally wide right-hand sales rail.

Use the following proportional relationships:

- **Main workspace:** ~72–76% of the content width.
- **Right sales rail:** ~24–28%.
- KPI row: 3 compact cards.
- Middle row: wide analytical card + recent activity card.
- Lower row: event/map card + calendar/engagement card.
- Right rail persists vertically and contains the ticket-sales summary.

Do not force every card to have equal height. The reference uses content-driven vertical sizing to create a controlled asymmetry.

---

## Signature Element

### Olive “data language”

The memorable design decision is the repeated use of **soft olive-green as a unified data language**.

The same visual family appears in:

- revenue bars
- ticket-sales bars
- progress indicators
- small status marks
- selected data points
- compact legends

This makes unrelated analytics modules feel like one system.

**Implementation rule:** when adding a new data visualization, first consider whether its primary series can use the olive scale before introducing another hue.

---

## Component Specifications

### 1. Top Navigation

**Structure**

- Left: small brand mark + wordmark.
- Center-left: pill-style primary navigation.
- Right: search, settings, avatar, user name, role, chevron.

**Selected item**

- Filled muted violet pill.
- White label.
- Compact horizontal padding.
- No heavy shadow.

**Unselected items**

- Transparent/light surface.
- Muted text.
- Small icon + label.

---

### 2. KPI Cards

Each KPI card is intentionally minimal:

```text
┌──────────────────────────────┐
│  [icon]                      │
│  Total Event                 │
│  32           ↑ +2 vs last month
└──────────────────────────────┘
```

Rules:

- Small icon at the top.
- Label below icon.
- Large number with strong weight.
- Delta aligned near the metric or baseline.
- Positive movement uses olive.
- Negative movement uses coral.
- Avoid decorative illustrations.

---

### 3. Revenue Breakdown

This is the primary analytical card.

**Composition**

- Small title + “See details” action.
- Rounded vertical bars.
- One thin line overlay for a secondary trend.
- Bottom summary strip with three compact metrics.

The chart should have **minimal axes**. The data bars provide the structure.

**Chart behavior**

- Rounded bar tops.
- Soft olive bars.
- Thin secondary line.
- Small dots where the line intersects data.
- No heavy gridlines.
- Use tooltips on hover/focus rather than persistent labels on every bar.

---

### 4. Recent Activities

A vertically stacked activity feed with an event thumbnail/tag area at the top.

Each activity row contains:

- status/event icon
- concise sentence
- timestamp
- optional avatar or participant mark

Use **sentence case** and active verbs.

Example structure:

```text
●  John created a new event: Music Fest
   Mon, Feb 24 · 3:12 PM

●  5 attendees requested refunds
   Mon, Feb 24 · 3:45 PM
```

The feed should read naturally rather than like a database log.

---

### 5. Ticket Sales Summary

The right rail is the most persistent analytical region.

Header:

- title
- large total
- small unit label

Then:

- category tabs
- ranked event rows
- compact thumbnail
- event name
- location/date
- ticket count
- small horizontal progress bar

Below:

**Ticket Sales Overtime**

- dense olive vertical bars
- category legend
- small KPI rows underneath

The rail should remain visually calm even though it contains significant information.

---

### 6. Event + Map Card

A split layout:

```text
┌───────────────────────────┬─────────────────────────┐
│ Event list                │                          │
│                           │      light map          │
│ Future of AI       $75    │      + marker           │
│ City Jazz Live      $60   │                          │
│                           │                          │
└───────────────────────────┴─────────────────────────┘
```

Map treatment:

- very light gray roads
- minimal labels
- one clear location marker
- no saturated map colors

The map is contextual, not the focal point.

---

### 7. Event Engagement Calendar

Use a compact month calendar.

- Tiny header with previous/next controls.
- Month label centered or left-aligned.
- Circular date cells.
- Coral intensity indicates engagement level.
- Strongest date is the darkest/most saturated.
- Low values fade toward a light tint.
- Preserve sufficient contrast for accessibility.

This is a **data heatmap disguised as a familiar calendar**, not a generic scheduling widget.

---

### 8. Cards and Containers

Cards should generally follow:

```css
background: #FFFFFF;
border: 1px solid #E8E8E4;
border-radius: 10px;
box-shadow: none;
```

Internal padding:

- desktop: 14–18px
- dense metric cards: 12–16px
- larger analytical cards: 16–20px

Maintain consistent title-to-content spacing.

---

## Imagery & Icons

### Imagery

Event thumbnails are small and colorful, creating controlled visual contrast against the monochrome dashboard.

Rules:

- Use imagery only where it adds identification value.
- Crop into consistent thumbnail sizes.
- Preserve recognizable event visuals.
- Do not add decorative stock imagery to empty areas.

### Icons

Use a thin, compact icon family with consistent stroke weight.

Icon size:

- navigation: 12–14px
- card utility: 10–12px
- activity status: 12–16px

Icons should support labels, not replace them when the action is ambiguous.

---

## Interaction & Motion

Motion should be subtle and informational.

Recommended:

- 120–180ms hover transitions on cards/buttons.
- Chart tooltip fade-in.
- Calendar cell hover/focus state.
- Activity row highlight on hover.
- Progress bars may animate once on initial render.

Avoid:

- bouncing cards
- floating dashboards
- parallax
- large entrance animations
- animated gradients

Respect `prefers-reduced-motion`.

---

## Responsive Behavior

### Desktop

Match the reference composition closely:

- persistent right sales rail
- 3 KPI cards across the top
- analytical cards arranged in an asymmetric grid

### Tablet

- collapse the right rail below the main analytics
- keep KPI cards in a two-column arrangement
- reduce horizontal padding slightly

### Mobile

Reflow into a single column:

```text
Top bar
KPI
KPI
KPI
Revenue
Activities
Ticket Summary
Events / Map
Engagement Calendar
Sales Overtime
```

The hierarchy should remain identical even though the geometry changes.

---

## Accessibility

- Body text should target WCAG-readable contrast against white.
- Never communicate positive/negative states by color alone.
- Every icon-only button needs an accessible name.
- Chart information needs an accessible text summary or data table alternative.
- Keyboard focus must be clearly visible.
- Touch targets should remain at least 44×44px on mobile.
- Avoid very light gray text for important information merely to match the screenshot.

---

## Content & Copy Rules

Follow the reference's concise operational tone.

Use:

- sentence case
- short labels
- active verbs
- real-world nouns
- direct actions such as `See details`, `Create event`, `View more`

Avoid:

- marketing slogans inside dashboard modules
- unnecessary helper text
- technical system terminology
- verbose descriptions under every metric

Numbers should be formatted consistently and use tabular numerals.

---

# Pass 2 — Review Against the Brief

### What makes this specific rather than generic

A generic SaaS dashboard would typically rely on blue/purple gradients, heavier shadows, identical card proportions, large hero KPIs, and decorative charts.

This design intentionally rejects those defaults.

Its identity comes from three linked decisions:

1. **Olive is the persistent analytics color**, creating a recognizable data language.
2. **The right-hand ticket-sales rail remains vertically dominant**, matching the operational needs of an event manager.
3. **Analytics use editorial restraint**: hairline borders, almost no shadows, tiny utility labels, and compact charts.

### Deliberate aesthetic risk

The main risk is the **olive-first palette**. Olive is less common than conventional blue SaaS colors and can initially feel unusual for enterprise analytics. That is intentional: it gives the product a recognizable identity while remaining calm enough for prolonged dashboard use.

Do not neutralize this decision by replacing olive with a generic blue.

### Elements to remove during implementation if they appear

Before finalizing the UI, remove any element that makes the result feel more generic than the reference:

- unnecessary gradients
- large empty hero areas
- oversized rounded cards
- decorative blobs
- excessive shadows
- duplicated legends
- labels that do not help interpretation
- color applied only for decoration
- excessive animation

---

# Implementation Checklist

- [ ] White canvas with subtle off-white supporting surfaces.
- [ ] Hairline borders instead of strong shadows.
- [ ] Olive data system established before chart implementation.
- [ ] Coral reserved for attention/change states.
- [ ] Violet reserved for selected/contextual controls.
- [ ] Compact type hierarchy with tabular numerals.
- [ ] 3 KPI cards at the top of the main workspace.
- [ ] Main analytics + recent activity split.
- [ ] Persistent right-hand ticket sales rail on desktop.
- [ ] Event list paired with a light contextual map.
- [ ] Calendar heatmap uses coral intensity.
- [ ] Consistent 8px spacing rhythm.
- [ ] Responsive one-column mobile hierarchy.
- [ ] Keyboard focus and reduced-motion support.
- [ ] Final visual pass removes unnecessary decoration.

---

## Source Reference

This document is derived from the supplied YowTix dashboard screenshot and should be treated as the visual source of truth for the dashboard's overall hierarchy, density, palette, card treatment, and chart language.

Where implementation constraints require deviation, preserve the **visual hierarchy and design intent** before preserving exact pixel measurements.
