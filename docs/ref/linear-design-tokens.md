---
version: alpha
name: "Linear Dual Theme System"
source: https://www.design-extractor.com/gallery/linear-638bvy
provenance: "Dark tokens follow the pinned Linear extraction; Light and semantic status tokens are CD211-derived counterparts."
themes:
  dark:
    colors:
      bg-primary: "#08090a"
      bg-secondary: "#0f1011"
      bg-elevated: "#23252a"
      text-primary: "#f7f8f8"
      text-secondary: "#d0d6e0"
      text-tertiary: "#8a8f98"
      text-quaternary: "#62666d"
      accent: "#5e6ad2"
      accent-hover: "#6c78de"
      accent-text: "#a1a8f0"
      accent-text-hover: "#c3c8ff"
      accent-soft: "rgba(94, 106, 210, 0.08)"
      accent-soft-hover: "rgba(94, 106, 210, 0.18)"
      accent-soft-disabled: "rgba(94, 106, 210, 0.04)"
      accent-border: "rgba(94, 106, 210, 0.32)"
      accent-border-strong: "rgba(94, 106, 210, 0.56)"
      border: "rgba(226, 228, 231, 0.09)"
      border-strong: "rgba(226, 228, 231, 0.16)"
      border-hover: "rgba(226, 228, 231, 0.3)"
      row-hover: "rgba(255, 255, 255, 0.02)"
      row-sticky-hover: "#0d0e10"
      ok: "#4cb782"
      ok-soft: "rgba(76, 183, 130, 0.08)"
      ok-border: "rgba(76, 183, 130, 0.4)"
      warn: "#f2c94c"
      warn-soft: "rgba(242, 201, 76, 0.08)"
      warn-border: "rgba(242, 201, 76, 0.4)"
      danger: "#eb5757"
      danger-soft: "rgba(235, 87, 87, 0.08)"
      danger-soft-hover: "rgba(235, 87, 87, 0.12)"
      danger-border: "rgba(235, 87, 87, 0.4)"
      danger-border-strong: "rgba(235, 87, 87, 0.52)"
      notice-danger-text: "#f0a6a6"
      notice-success-text: "#9ed8bc"
      notice-warning-text: "#e9cf83"
    elevation:
      shadow-low: "0px 2px 4px 0px rgba(0, 0, 0, 0.4)"
      shadow-border-inset: "inset 0px 0px 0px 1px rgb(35, 37, 42)"
      shadow-sticky: "-12px 0 16px -16px rgba(0, 0, 0, 0.9)"
      shadow-dialog: "0 18px 60px rgba(0, 0, 0, 0.62)"
      dialog-backdrop: "rgba(3, 4, 5, 0.72)"
  light:
    colors:
      bg-primary: "#f8f9fb"
      bg-secondary: "#f1f2f4"
      bg-elevated: "#e8eaed"
      text-primary: "#202124"
      text-secondary: "#42464d"
      text-tertiary: "#686d76"
      text-quaternary: "#8a9099"
      accent: "#4f5bc4"
      accent-hover: "#424ead"
      accent-text: "#4f5bc4"
      accent-text-hover: "#38449f"
      accent-soft: "rgba(79, 91, 196, 0.1)"
      accent-soft-hover: "rgba(79, 91, 196, 0.16)"
      accent-soft-disabled: "rgba(79, 91, 196, 0.05)"
      accent-border: "rgba(79, 91, 196, 0.3)"
      accent-border-strong: "rgba(79, 91, 196, 0.5)"
      border: "rgba(32, 33, 36, 0.1)"
      border-strong: "rgba(32, 33, 36, 0.18)"
      border-hover: "rgba(32, 33, 36, 0.3)"
      row-hover: "rgba(32, 33, 36, 0.035)"
      row-sticky-hover: "#eff0f2"
      ok: "#247a52"
      ok-soft: "rgba(36, 122, 82, 0.09)"
      ok-border: "rgba(36, 122, 82, 0.38)"
      warn: "#8a5a00"
      warn-soft: "rgba(138, 90, 0, 0.09)"
      warn-border: "rgba(138, 90, 0, 0.38)"
      danger: "#c23b3b"
      danger-soft: "rgba(194, 59, 59, 0.08)"
      danger-soft-hover: "rgba(194, 59, 59, 0.13)"
      danger-border: "rgba(194, 59, 59, 0.38)"
      danger-border-strong: "rgba(194, 59, 59, 0.5)"
      notice-danger-text: "#9f2f2f"
      notice-success-text: "#19683f"
      notice-warning-text: "#7a5200"
    elevation:
      shadow-low: "0px 2px 4px 0px rgba(32, 33, 36, 0.12)"
      shadow-border-inset: "inset 0px 0px 0px 1px rgba(32, 33, 36, 0.12)"
      shadow-sticky: "-12px 0 16px -16px rgba(32, 33, 36, 0.24)"
      shadow-dialog: "0 18px 60px rgba(32, 33, 36, 0.2)"
      dialog-backdrop: "rgba(32, 33, 36, 0.4)"
shared:
  colors:
    button-on-accent: "#ffffff"
source-reference:
  white-surface: "#ffffff"
  border-subtle: "#e2e4e7"
  shadow-hairline: "0px 1.2px 0px 0px rgba(0, 0, 0, 0.03)"
  shadow-inset-overlay: "inset 0px 0px 12px 0px rgba(0, 0, 0, 0.2)"
  shadow-border-outer: "0px 0px 0px 1px rgba(0, 0, 0, 0.2)"
typography:
  families:
    font-sans-en: '"Inter Variable", "Inter", "SF Pro Display", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Oxygen, Ubuntu, Cantarell, "Open Sans", "Helvetica Neue", sans-serif'
    font-sans-zh: '"Inter Variable", "Inter", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Noto Sans CJK SC", "Source Han Sans SC", "SF Pro Display", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif'
    font-mono-en: '"Berkeley Mono", ui-monospace, "SF Mono", Menlo, monospace'
    font-mono-zh: '"Berkeley Mono", ui-monospace, "SF Mono", Menlo, "Noto Sans Mono CJK SC", "Sarasa Mono SC", "PingFang SC", "Microsoft YaHei", monospace'
  styles:
    display-hero: { fontSize: "64px", fontWeight: 510, lineHeight: "1.1", letterSpacing: "-0.88px" }
    title-1: { fontSize: "40px", fontWeight: 510, lineHeight: "44px", letterSpacing: "-0.88px" }
    title-2: { fontSize: "18px", fontWeight: 400, lineHeight: "28.8px", letterSpacing: "-0.165px" }
    body-base: { fontSize: "16px", fontWeight: 400, lineHeight: "24px" }
    body-regular: { fontSize: "15px", fontWeight: 400, lineHeight: "24px", letterSpacing: "-0.165px" }
    code-inline: { fontSize: "14px", fontWeight: 400, lineHeight: "24px" }
    label-medium: { fontSize: "13px", fontWeight: 510, lineHeight: "19.5px", letterSpacing: "-0.13px" }
    nav-item: { fontSize: "13px", fontWeight: 400, lineHeight: "19.5px", letterSpacing: "-0.13px" }
    label-small: { fontSize: "12px", fontWeight: 510, lineHeight: "16.8px" }
    caption: { fontSize: "10px", fontWeight: 510, lineHeight: "15px" }
rounded:
  radius-xs: "2px"
  radius-sm: "4px"
  radius-md: "6px"
  radius-lg: "8px"
  radius-xl: "12px"
  radius-2xl: "16px"
  radius-pill: "9999px"
spacing:
  spacing-1: "2px"
  spacing-2: "4px"
  spacing-3: "6px"
  spacing-4: "8px"
  spacing-5: "12px"
  spacing-6: "16px"
  spacing-7: "20px"
  spacing-8: "24px"
  spacing-9: "32px"
  spacing-10: "48px"
  spacing-11: "96px"
---

## Overview

CD211 uses one dark-first, high-density visual language with two color themes. The Dark theme is anchored to the pinned Linear extraction. The Light theme is a CD211-derived semantic counterpart: it preserves the same surface hierarchy, restrained indigo emphasis, typography, spacing, geometry, and elevation model without claiming to be part of the external source.

The YAML frontmatter is the canonical token inventory. The tables below explain roles and implementation behavior.

**Signature traits:**

- Inter-first Latin typography with explicit Simplified Chinese fallbacks; Berkeley Mono-first code and data typography.
- Micro-radius component geometry: 2–6px dominant; pill geometry reserved for badges and status chips.
- Layered elevation through subtle borders and low-opacity shadows, not dramatic floating surfaces.
- A 4px base grid and high-density composition.
- One restrained indigo primary action per screen.

## Theme Model

- `:root` is the Light/default theme in the current application.
- `html[data-theme="dark"]` applies the Dark theme.
- Semantic roles remain stable when themes switch: `bg-primary` is always the page surface, `text-primary` is always the highest-emphasis text, and `accent` is always the primary interactive color.
- Typography, spacing, radii, and component geometry are shared across themes.
- Dark is the source visual anchor. Light colors and all green/yellow/red workflow status colors are CD211-derived additions.
- `button-on-accent` is shared white text for primary indigo buttons in both themes.
- `source-reference` retains extracted Linear values that inform the system but are not exposed as current CSS custom properties.

### Preserved Linear source references

| Token | Value | Role |
|---|---|---|
| `white-surface` | `#ffffff` | White source anchor and overlay basis |
| `border-subtle` | `#e2e4e7` | Raw dark-theme divider source before opacity mapping |
| `shadow-hairline` | `0px 1.2px 0px 0px rgba(0, 0, 0, 0.03)` | Hairline depth |
| `shadow-inset-overlay` | `inset 0px 0px 12px 0px rgba(0, 0, 0, 0.2)` | Inset overlay depth |
| `shadow-border-outer` | `0px 0px 0px 1px rgba(0, 0, 0, 0.2)` | Outer border depth |

## Colors — Dark Theme

### Surfaces and text

| Token | Value | Role |
|---|---|---|
| `bg-primary` | `#08090a` | Page-level background and deepest surface |
| `bg-secondary` | `#0f1011` | Secondary panels and table surfaces |
| `bg-elevated` | `#23252a` | Sidebar, dialogs, and elevated controls |
| `text-primary` | `#f7f8f8` | Primary headings and body text |
| `text-secondary` | `#d0d6e0` | Secondary labels, navigation, and subheadings |
| `text-tertiary` | `#8a8f98` | Muted metadata and placeholders |
| `text-quaternary` | `#62666d` | Disabled and faint labels |

### Accent, borders, and rows

| Token | Value | Role |
|---|---|---|
| `accent` / `accent-hover` | `#5e6ad2` / `#6c78de` | Primary action and hover state |
| `accent-text` / `accent-text-hover` | `#a1a8f0` / `#c3c8ff` | Indigo links on dark surfaces |
| `accent-soft` / `accent-soft-hover` / `accent-soft-disabled` | `rgba(94, 106, 210, 0.08)` / `0.18` / `0.04` alpha | Tinted control states |
| `accent-border` / `accent-border-strong` | `rgba(94, 106, 210, 0.32)` / `0.56` alpha | Accent outlines and emphasis |
| `border` / `border-strong` / `border-hover` | `rgba(226, 228, 231, 0.09)` / `0.16` / `0.3` alpha | Hairlines and interaction emphasis |
| `row-hover` / `row-sticky-hover` | `rgba(255, 255, 255, 0.02)` / `#0d0e10` | Table-row hover surfaces |

### Workflow status

| Role | Main | Soft surface | Border | Notice text |
|---|---|---|---|---|
| Success | `#4cb782` | `rgba(76, 183, 130, 0.08)` | `rgba(76, 183, 130, 0.4)` | `#9ed8bc` |
| Warning | `#f2c94c` | `rgba(242, 201, 76, 0.08)` | `rgba(242, 201, 76, 0.4)` | `#e9cf83` |
| Danger | `#eb5757` | `rgba(235, 87, 87, 0.08)`; hover `0.12` | `rgba(235, 87, 87, 0.4)`; strong `0.52` | `#f0a6a6` |

## Colors — Light Theme

### Surfaces and text

| Token | Value | Role |
|---|---|---|
| `bg-primary` | `#f8f9fb` | Page-level background |
| `bg-secondary` | `#f1f2f4` | Secondary panels and table surfaces |
| `bg-elevated` | `#e8eaed` | Sidebar, dialogs, and elevated controls |
| `text-primary` | `#202124` | Primary headings and body text |
| `text-secondary` | `#42464d` | Secondary labels, navigation, and subheadings |
| `text-tertiary` | `#686d76` | Muted metadata and placeholders |
| `text-quaternary` | `#8a9099` | Disabled and faint labels |

### Accent, borders, and rows

| Token | Value | Role |
|---|---|---|
| `accent` / `accent-hover` | `#4f5bc4` / `#424ead` | Primary action and hover state |
| `accent-text` / `accent-text-hover` | `#4f5bc4` / `#38449f` | Indigo links on light surfaces |
| `accent-soft` / `accent-soft-hover` / `accent-soft-disabled` | `rgba(79, 91, 196, 0.1)` / `0.16` / `0.05` alpha | Tinted control states |
| `accent-border` / `accent-border-strong` | `rgba(79, 91, 196, 0.3)` / `0.5` alpha | Accent outlines and emphasis |
| `border` / `border-strong` / `border-hover` | `rgba(32, 33, 36, 0.1)` / `0.18` / `0.3` alpha | Hairlines and interaction emphasis |
| `row-hover` / `row-sticky-hover` | `rgba(32, 33, 36, 0.035)` / `#eff0f2` | Table-row hover surfaces |

### Workflow status

| Role | Main | Soft surface | Border | Notice text |
|---|---|---|---|---|
| Success | `#247a52` | `rgba(36, 122, 82, 0.09)` | `rgba(36, 122, 82, 0.38)` | `#19683f` |
| Warning | `#8a5a00` | `rgba(138, 90, 0, 0.09)` | `rgba(138, 90, 0, 0.38)` | `#7a5200` |
| Danger | `#c23b3b` | `rgba(194, 59, 59, 0.08)`; hover `0.13` | `rgba(194, 59, 59, 0.38)`; strong `0.5` | `#9f2f2f` |

## Typography

No font files are bundled. These tokens are ordered fallback stacks; the first installed family containing a glyph is used.

| Token | Use | Stack behavior |
|---|---|---|
| `font-sans-en` | English UI | Inter Variable/Inter first, then Apple, Windows, Linux, and generic sans-serif fallbacks |
| `font-sans-zh` | Simplified Chinese and mixed UI | Inter remains first for Latin glyphs; PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans CJK SC, and Source Han Sans SC provide explicit Chinese fallbacks |
| `font-mono-en` | English code, hashes, paths, and numeric data | Berkeley Mono first, then platform monospace families |
| `font-mono-zh` | Mixed Chinese code/data regions | Keeps the Latin monospace prefix, then uses Noto Sans Mono CJK SC, Sarasa Mono SC, PingFang SC, or Microsoft YaHei for Chinese glyphs |

English pages alias `font-sans` and `font-mono` to the English stacks. Pages with `lang="zh"` alias them to the Chinese-capable stacks. Chinese body copy uses normal letter spacing; negative tracking remains a Latin typography detail and is not applied globally to CJK text. Deliberate component-level tracking for technical labels may still override the body default.

The shared type scale is:

| Style | Size | Weight | Line height | Latin letter spacing |
|---|---:|---:|---:|---:|
| `display-hero` | 64px | 510 | 1.1 | -0.88px |
| `title-1` | 40px | 510 | 44px | -0.88px |
| `title-2` | 18px | 400 | 28.8px | -0.165px |
| `body-base` | 16px | 400 | 24px | normal |
| `body-regular` | 15px | 400 | 24px | -0.165px |
| `code-inline` | 14px | 400 | 24px | normal |
| `label-medium` | 13px | 510 | 19.5px | -0.13px |
| `nav-item` | 13px | 400 | 19.5px | -0.13px |
| `label-small` | 12px | 510 | 16.8px | normal |
| `caption` | 10px | 510 | 15px | normal |

## Layout and Geometry

- Base grid: 4px; allowed scale values are 2, 4, 6, 8, 12, 16, 20, 24, 32, 48, and 96px.
- Breakpoints: ≤600, ≤640, ≤768, ≤1024, and ≤1280px, plus `(hover: none) and (pointer: coarse)`.
- Mobile: constrain layout and stack content vertically. Desktop: expand density and use horizontal composition.

| Radius token | Value | Role |
|---|---:|---|
| `radius-xs` | 2px | Hairline corner |
| `radius-sm` | 4px | Buttons, inputs, and subtle corners |
| `radius-md` | 6px | Controls and cards |
| `radius-lg` | 8px | Larger controls |
| `radius-xl` | 12px | Reserved large control corner |
| `radius-2xl` | 16px | Reserved card corner |
| `radius-pill` | 9999px | Badges and status chips only |

## Elevation and Depth

| Token | Dark | Light | Role |
|---|---|---|---|
| `shadow-low` | `0px 2px 4px 0px rgba(0, 0, 0, 0.4)` | `0px 2px 4px 0px rgba(32, 33, 36, 0.12)` | Low elevated surface |
| `shadow-border-inset` | `inset 0px 0px 0px 1px rgb(35, 37, 42)` | `inset 0px 0px 0px 1px rgba(32, 33, 36, 0.12)` | Inset edge definition |
| `shadow-sticky` | `-12px 0 16px -16px rgba(0, 0, 0, 0.9)` | `-12px 0 16px -16px rgba(32, 33, 36, 0.24)` | Sticky action-column separation |
| `shadow-dialog` | `0 18px 60px rgba(0, 0, 0, 0.62)` | `0 18px 60px rgba(32, 33, 36, 0.2)` | Dialog elevation |
| `dialog-backdrop` | `rgba(3, 4, 5, 0.72)` | `rgba(32, 33, 36, 0.4)` | Modal backdrop |

Interaction signals use restrained backdrop blur and high-contrast focus outlines. Do not invent stronger shadows to replace border hierarchy.

## Do's and Don'ts

| Do | Don't |
|---|---|
| Preserve semantic roles when switching themes | Treat Light as an inverted Dark palette |
| Use the 4px spacing grid and 2–6px dominant radii | Mix rounded and sharp geometry in one view |
| Use explicit English and Simplified Chinese fallback stacks | Claim an unbundled font is guaranteed to render |
| Keep the primary color for the single most important action per screen | Spread indigo across unrelated controls |
| Maintain WCAG AA contrast as an implementation requirement | Claim the token sheet alone is a completed accessibility audit |
| Reuse the documented elevation tokens | Invent shadows beyond this evidence |
