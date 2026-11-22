---
name: Clumoove
description: Flat, monochrome operations console for self-hosted cloud migration, sync and backup.
colors:
  ink: "#18181b"
  graphite: "#27272a"
  slate: "#52525b"
  ash: "#71717a"
  paper: "#fafafa"
  sheet: "#ffffff"
  wash: "#f4f4f5"
  rule: "#e4e4e7"
  focus-blue: "#2563eb"
  success: "#15803d"
  success-wash: "#f0fdf4"
  warning: "#a16207"
  warning-wash: "#fffbeb"
  error: "#b91c1c"
  error-wash: "#fef2f2"
  info: "#1d4ed8"
  info-wash: "#eff6ff"
typography:
  headline:
    fontFamily: "Poppins, system-ui, -apple-system, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: "1.75rem"
  title:
    fontFamily: "Poppins, system-ui, -apple-system, sans-serif"
    fontSize: "1.125rem"
    fontWeight: 600
    lineHeight: "1.75rem"
    letterSpacing: "-0.025em"
  body:
    fontFamily: "Open Sans, system-ui, -apple-system, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: "1.25rem"
  label:
    fontFamily: "Open Sans, system-ui, -apple-system, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 700
    lineHeight: "1rem"
    letterSpacing: "0.05em"
  meta:
    fontFamily: "Open Sans, system-ui, -apple-system, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 400
    lineHeight: "1rem"
rounded:
  sm: "2px"
  md: "4px"
  lg: "6px"
  xl: "8px"
  full: "9999px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "16px"
  lg: "24px"
components:
  button-primary:
    backgroundColor: "{colors.ink}"
    textColor: "{colors.sheet}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "8px 12px"
  button-primary-hover:
    backgroundColor: "{colors.graphite}"
  button-secondary:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.slate}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "8px 12px"
  button-secondary-hover:
    backgroundColor: "{colors.wash}"
    textColor: "{colors.ink}"
  button-danger:
    backgroundColor: "{colors.error}"
    textColor: "{colors.sheet}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "8px 12px"
  input:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.ink}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "8px 12px"
  card:
    backgroundColor: "{colors.sheet}"
    textColor: "{colors.ink}"
    rounded: "{rounded.lg}"
    padding: "24px"
  badge-success:
    backgroundColor: "{colors.success-wash}"
    textColor: "{colors.success}"
    typography: "{typography.meta}"
    rounded: "{rounded.md}"
    padding: "4px 10px"
  badge-error:
    backgroundColor: "{colors.error-wash}"
    textColor: "{colors.error}"
    typography: "{typography.meta}"
    rounded: "{rounded.md}"
    padding: "4px 10px"
  badge-muted:
    backgroundColor: "{colors.wash}"
    textColor: "{colors.slate}"
    typography: "{typography.meta}"
    rounded: "{rounded.md}"
    padding: "4px 10px"
  alert-error:
    backgroundColor: "{colors.error-wash}"
    textColor: "{colors.error}"
    typography: "{typography.body}"
    rounded: "{rounded.md}"
    padding: "8px 12px"
---

# Design System: Clumoove

## Overview

**Creative North Star: "The Operations Ledger"**

Clumoove is a record of work being done, and the interface reads like one: hairline rules, tabular figures, plain
status words, and surfaces that stay still while the numbers move. An operator leaves a migration running overnight and
comes back to a page that tells them exactly what happened. The system is sober, exact and trustworthy. It does not
perform confidence; it shows evidence.

The palette is a single zinc neutral ramp, with color reserved for state: green for verified, amber for needs
attention, red for failed, blue for "your keyboard is here". Depth comes from borders and tonal fills, never shadows.
Corners are barely rounded (2 to 8px) so the product feels like infrastructure rather than a consumer app. Light and
dark themes are equal citizens and share one token vocabulary.

Anti-references, confirmed by the codebase's own rules: the retired `portal-*` and `glass-*` styling, decorative
gradient overlays, heavy backdrop blur, oversized shadows and scaling hover effects.

**Key Characteristics:**
- Flat by default: 1px borders and tonal fills carry all structure.
- One neutral ramp; chromatic color only ever means state or focus.
- Poppins for headings, Open Sans for everything else; 12px is the floor.
- Dense but breathable: 8px base rhythm, 24px card padding, `max-w-6xl` page column.
- Two themes (`data-theme="light|dark"`), one set of semantic tokens.
- Motion is minimal (a 180ms fade on view change, 500ms progress-width) and fully removed under `prefers-reduced-motion`.

## Colors

A cold zinc ramp with four state hues. Light-mode values are normative in the frontmatter; the dark theme remaps the
same semantic tokens (`--color-*` in `frontend/src/index.css`).

### Primary
- **Ink** (#18181b): The primary action fill, body text, the brand mark and the selected-tab underline. It is the system's
  only "accent", and it is black. In dark mode the same role is played by near-white text on `#18181b`/`#27272a`.
- **Graphite** (#27272a): Hover state for primary buttons and the `--color-primary` theme token.

### Secondary
- **Focus Blue** (#2563eb, dark: #93c5fd): Keyboard focus ring only (2px solid, 2px offset), plus the `Toggle` switch's
  "on" track.

### Neutral
- **Paper** (#fafafa): Page background (`bg-primary`). Dark: #18181b.
- **Sheet** (#ffffff): Cards, inputs, menus and the sticky header (`bg-secondary`/`bg-elevated`). Dark: #27272a.
- **Wash** (#f4f4f5): Hover fill, table headers, muted badges, toggle off-track. Dark: #3f3f46.
- **Rule** (#e4e4e7): Every 1px border, divider and progress track. Dark: #3f3f46.
- **Slate** (#52525b): Secondary text and secondary-button labels. Dark: #d4d4d8.
- **Ash** (#71717a): Muted text, placeholders, metadata. Dark: #a1a1aa.

### State
- **Verified Green** (#15803d on #f0fdf4): Success, completed, verified integrity.
- **Caution Amber** (#a16207 on #fffbeb): Warnings and paused-with-reason states; badge text stays `ink` on the amber wash.
- **Failure Red** (#b91c1c on #fef2f2): Errors, failed transfers, destructive button fill.
- **Info Blue** (#1d4ed8 on #eff6ff): Neutral informational notices.

File-type icons have their own small semantic set (`--color-file-image`, `-video`, `-audio`, `-document`, `-archive`,
`-folder`) that is lightened in dark mode; it is not part of the interface palette.

### Named Rules
**The Color-Means-State Rule.** Chromatic color appears only to report status or mark focus. Decoration is neutral.

**The Token-Only Rule.** Use `var(--color-*)` semantic tokens, never raw Tailwind palette colors, so both themes stay in
lockstep. The switch "on" track is `--color-toggle-on`.

## Typography

**Display Font:** Poppins (with system-ui, -apple-system, sans-serif)
**Body Font:** Open Sans (with system-ui, -apple-system, sans-serif)
**Label/Mono Font:** Open Sans. `--font-mono` is deliberately aliased to Open Sans; figures are emphasized by weight, not by a monospace face.

**Character:** Poppins gives headings a clean geometric authority; Open Sans keeps dense data and German-length labels
readable. Both are self-hosted (`/fonts/*.woff2`, `font-display: swap`) so no request leaves the operator's
infrastructure.

### Hierarchy
- **Headline** (Poppins 600, 1.25rem, 1.75rem line): Screen titles on auth and error surfaces.
- **Title** (Poppins 600, 1.125rem, 1.75rem line, -0.025em tracking): Brand wordmark, section and card titles.
- **Body** (Open Sans 400, 0.875rem, 1.25rem line): Default UI text, buttons (weight 500), table cells.
- **Label** (Open Sans 700, 0.75rem, 0.05em tracking, uppercase for group headings; 0.875rem/600 for form labels): Field labels, stat-group headings, tab text.
- **Meta** (Open Sans 400, 0.75rem, 1rem line): Timestamps, hints, badge text, secondary figures.

### Named Rules
**The Twelve-Pixel Floor Rule.** Nothing renders below 0.75rem. A global rule lifts legacy `text-[9px]` to `text-[11px]` values to 12px.

**The Locale-Length Rule.** Every string exists in `de` and `en`; layouts must tolerate German being roughly a third longer.

## Layout

A single centered column, `max-w-6xl` (72rem) with 16px side padding (24px from `sm`), below a 64px sticky header and above a
right-aligned footer. Content stacks in vertical sections separated by 24px (`space-y-6`), with card interiors at 24px padding and
control groups at 8px gaps (`gap-2`). Two-up data panels use `grid-cols-1 md:grid-cols-2` with 24px gaps. View changes fade in
over 180ms.

Tables collapse below 768px into stacked rows: the header is visually hidden and each cell shows its `data-label` as a 12px bold
muted prefix (`ui-responsive-table`). Auth and reset screens vertically center their card; every other view starts at the top.
The page scrollbar gutter is reserved (`scrollbar-gutter: stable`) so content never shifts.

Z-order is tokenized: sticky 20, menu 30, dialog 40, toast 50.

## Elevation & Depth

No shadows. `--shadow-sm` through `--shadow-2xl` are all set to `none`, so even a stray Tailwind `shadow-*` class renders flat.
Depth is conveyed in three ways: a 1px `Rule` border around every raised surface, a tonal step between page (`Paper`) and
surface (`Sheet`), and a `--color-overlay` scrim (ink at 55% light, 72% dark) behind dialogs.

### Named Rules
**The Flat-By-Default Rule.** Surfaces are flat at rest and flat on hover. State is a fill change or an underline, never a shadow, glow or scale.

## Shapes

Barely-rounded rectangles. The radius scale is 2 / 4 / 6 / 8px; `2xl` and `3xl` are capped at 8px so nothing can become pillowy.
Controls (buttons, inputs, selects, badges, alerts) use 4px. Cards and sections use 6px. Panels and dialogs may use 8px.
Only three things are fully round: avatars, the progress track, and the toggle switch. Borders are always 1px solid `Rule`;
the single exception is the 2px selected-tab underline.

## Components

### Buttons
- **Shape:** Square-ish, 4px radius.
- **Primary:** `Ink` fill, white text, 8px 12px padding (`px-3 py-2`), 14px weight 500, icon + label at 8px gap. Hover shifts to `Graphite` via the utility itself; never add `hover:opacity-*`. Disabled: 55% opacity, `not-allowed`. Loading shows an inline spinner and sets `aria-busy`.
- **Secondary:** `Sheet` fill, 1px `Rule` border, `Slate` text; hover fills `Wash` and promotes text to `Ink`. The default variant.
- **Danger:** `Failure Red` fill, white text; reserved for destructive confirmation.
- **Quiet:** No fill or border, `Slate` text.
- **Icon:** Secondary styling, square padding (6 or 8px); always carries a localized `aria-label` and `title` (the `Button` component enforces this at the type level).
- **Sizes:** `md` 14px, `sm` 12px with 10px/6px padding.

### Badges and Alerts
- **Badge:** 4px radius, 1px border, 12px weight 500 on the state wash. Variants: success, error, warning, info, muted. An optional `pulse` marks live work.
- **Alert / MessageBanner:** Same wash + border + text triple as the badge, 12px 8px padding, centered 14px text. Always paired with a text status, never color alone.

### Cards / Containers
- **Corner Style:** 6px.
- **Background:** `Sheet` (`ui-card`), or `Paper` for nested `ui-section`.
- **Shadow Strategy:** None; see Elevation.
- **Border:** 1px `Rule`.
- **Internal Padding:** 24px for primary cards, 16px for dense ones.

### Inputs / Fields
- **Style:** `Sheet` fill, 1px `Rule` border, 4px radius, `Ink` text, `Ash` placeholder at full opacity.
- **Focus:** 2px `Focus Blue` outline with 2px offset (global `:focus-visible`).
- **Error / Disabled:** `Field` wires label, hint and error to the control (`aria-describedby`, `aria-errormessage`, `aria-invalid`); errors are 14px `Failure Red` text with `role="alert"`. Disabled controls drop to 50% opacity.

### Navigation
A 64px sticky header on `Sheet` with a bottom `Rule`: the brand mark (an `Ink`-filled mask of the logo SVG, 32px) with the Poppins
wordmark at left, the user menu at right. The menu is a 192px-wide flat panel (1px border, no shadow) with 14px `Slate` items that
tint `Wash` on hover; the sign-out item tints red. Tabs are text with a 2px transparent bottom border that becomes `Ink` when active
(weight 600); they follow the ARIA tab keyboard model with a roving tab stop.

### Progress
A fully rounded 8px-tall track in `Rule`, filled with `Ink` by default and `Verified Green` / `Caution Amber` / `Failure Red` for terminal
or warning states. Width animates over 500ms ease-out; status is mirrored to an `aria-live="polite"` text for screen readers.

### Dialogs
A fixed full-screen scrim with a centered `max-w-md` panel on `Paper` (`ui-section`), 1px border, 6px radius. Dialogs set an accessible title and
description, trap focus, close on Escape, lock body scroll, focus the safe action (Cancel) on open, and restore focus on close.

### File rows and icons
Directory and file listings tint their icons with the file-type color set (`ui-file-*`) and set metadata in `Ash` at 12px (`ui-meta`). Provider brand
logos (via `react-icons`) appear only in the connection flow.

## Do's and Don'ts

### Do:
- **Do** use the semantic tokens (`var(--color-bg-secondary)`, `var(--color-text-muted)`, ...) and the `ui-*` utilities from `frontend/src/index.css` for every new surface.
- **Do** express state with a text label plus color (badge, alert); never color alone.
- **Do** keep 4px radius on controls, 6px on cards, and 1px `Rule` borders on every raised surface.
- **Do** use `@heroicons/react` for interface icons, with a localized `aria-label` and `title` on icon-only buttons.
- **Do** format numbers, dates and sizes through `src/utils/format.ts` with the active locale.
- **Do** keep `de` and `en` strings in parity and allow roughly 30% extra width for German.
- **Do** honor `prefers-reduced-motion`; new animation must collapse to none.

### Don't:
- **Don't** reintroduce `portal-*`, `glass-*`, `shadow-portal*`, decorative gradient overlays or heavy backdrop blur.
- **Don't** add shadows, glows or scale-on-hover; depth is border plus tonal fill.
- **Don't** use raw Tailwind palette colors (`blue-600`, `green-500`) where a semantic token exists.
- **Don't** introduce a decorative accent hue; chromatic color is reserved for state and focus.
- **Don't** render text below 12px.
- **Don't** use `react-icons` outside provider brand logos.
- **Don't** round beyond 8px, except avatars, the progress track and the toggle.
