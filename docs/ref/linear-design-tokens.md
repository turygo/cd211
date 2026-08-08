---
version: alpha
name: "Linear Dark System"
source: https://www.design-extractor.com/gallery/linear-638bvy
description: "Linear's design system is a dark-first, high-density product UI built for engineering teams. The surface palette anchors on near-black (#08090a, #0f1011) with layered dark grays for sidebar, card, and panel differentiation. Typography is exclusively Inter Variable with precise negative letter-spacing at display sizes and Berkeley Mono for inline code. The radius language is deliberately small (2-6px dominant) with pill shapes reserved for badges and status chips. Elevation is expressed through subtle 1px inset borders and low-opacity drop shadows rather than dramatic layering. The primary CTA color is Linear's signature indigo (#5e6ad2), used on the Sign Up button and key links."
colors:
  brand-indigo: "#5e6ad2"
  background-primary: "#08090a"
  background-secondary: "#0f1011"
  background-elevated: "#23252a"
  white-surface: "#ffffff"
  text-primary: "#f7f8f8"
  text-secondary: "#d0d6e0"
  text-tertiary: "#8a8f98"
  text-quaternary: "#62666d"
  border-subtle: "#e2e4e7"
typography:
  display-hero:
    fontFamily: "Inter Variable"
    fontSize: "64px"
    fontWeight: "510"
    lineHeight: "1.1"
    letterSpacing: "-0.88px"
  title-1:
    fontFamily: "Inter Variable"
    fontSize: "40px"
    fontWeight: "510"
    lineHeight: "44px"
    letterSpacing: "-0.88px"
  title-2:
    fontFamily: "Inter Variable"
    fontSize: "18px"
    fontWeight: "400"
    lineHeight: "28.8px"
    letterSpacing: "-0.165px"
  body-base:
    fontFamily: "Inter Variable"
    fontSize: "16px"
    fontWeight: "400"
    lineHeight: "24px"
  body-regular:
    fontFamily: "Inter Variable"
    fontSize: "15px"
    fontWeight: "400"
    lineHeight: "24px"
    letterSpacing: "-0.165px"
  code-inline:
    fontFamily: "Berkeley Mono"
    fontSize: "14px"
    fontWeight: "400"
    lineHeight: "24px"
  label-medium:
    fontFamily: "Inter Variable"
    fontSize: "13px"
    fontWeight: "510"
    lineHeight: "19.5px"
    letterSpacing: "-0.13px"
  nav-item:
    fontFamily: "Inter Variable"
    fontSize: "13px"
    fontWeight: "400"
    lineHeight: "19.5px"
    letterSpacing: "-0.13px"
  label-small:
    fontFamily: "Inter Variable"
    fontSize: "12px"
    fontWeight: "510"
    lineHeight: "16.8px"
  caption:
    fontFamily: "Inter Variable"
    fontSize: "10px"
    fontWeight: "510"
    lineHeight: "15px"
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
shadows:
  shadow-hairline: "0px 1.2px 0px 0px rgba(0, 0, 0, 0.03)"
  shadow-low: "0px 2px 4px 0px rgba(0, 0, 0, 0.4)"
  shadow-inset-overlay: "inset 0px 0px 12px 0px rgba(0, 0, 0, 0.2)"
  shadow-border-inset: "inset 0px 0px 0px 1px rgb(35, 37, 42)"
  shadow-border-outer: "0px 0px 0px 1px rgba(0, 0, 0, 0.2)"
---

## Overview

Linear's design system is a dark-first, high-density product UI built for engineering teams. The surface palette anchors on near-black (#08090a, #0f1011) with layered dark grays for sidebar, card, and panel differentiation. Typography is exclusively Inter Variable with precise negative letter-spacing at display sizes and Berkeley Mono for inline code. The radius language is deliberately small (2-6px dominant) with pill shapes reserved for badges and status chips. Elevation is expressed through subtle 1px inset borders and low-opacity drop shadows rather than dramatic layering. The primary CTA color is Linear's signature indigo (#5e6ad2).

**Signature traits:**
- Dual typeface system: Inter Variable for UI, Berkeley Mono (fallback: ui-monospace, SF Mono, Menlo) for code/data.
- Micro-radius component geometry: 2-6px dominant; pill (9999px) reserved for badges and status chips.
- Layered elevation via 1px inset borders and low-opacity shadows, not dramatic layering.
- Font stack: `Inter Variable, SF Pro Display, -apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Oxygen, Ubuntu, Cantarell, Open Sans, Helvetica Neue, sans-serif` with features `"cv01", "ss03"`.
- Mono stack: `Berkeley Mono, ui-monospace, SF Mono, Menlo, monospace`.

## Colors (Dark Theme — primary)

| Token | Hex | Usage |
|-------|-----|-------|
| brand-indigo | #5e6ad2 | Primary CTA button, key interactive links |
| background-primary | #08090a | Page-level background, deepest surface layer |
| background-secondary | #0f1011 | Secondary surface, panel and card fills |
| background-elevated | #23252a | Elevated panels, sidebar, and modal surfaces |
| white-surface | #ffffff | Toast/notification backgrounds, modal overlays (low alpha) |
| text-primary | #f7f8f8 | Primary headings and body text on dark surfaces |
| text-secondary | #d0d6e0 | Secondary labels, nav items, subheadings |
| text-tertiary | #8a8f98 | Placeholder text, muted metadata, timestamps |
| text-quaternary | #62666d | Disabled states, faintest UI labels |
| border-subtle | #e2e4e7 | Hairline dividers (used at low alpha on dark surfaces) |

Semantic mapping: surface-background → background-primary; surface-text → text-primary; content-text → text-secondary; border-border → border-subtle.

## Layout

- 4px base grid; scale values 2, 4, 6, 8, 12, 16, 20, 24, 32, 48, 96.
- Breakpoints: <=600, <=640, <=768, <=1024, <=1280, plus `(hover: none) and (pointer: coarse)`.
- Mobile (<=1280px): constrain layout, vertical stacking. Desktop: expand density, horizontal composition.

## Elevation & Depth

| Shadow Token | Details |
|--------------|---------|
| shadow-hairline | 0px 1.2px 0px 0px rgba(0, 0, 0, 0.03) |
| shadow-low | 0px 2px 4px 0px rgba(0, 0, 0, 0.4) |
| shadow-inset-overlay | inset 0px 0px 12px 0px rgba(0, 0, 0, 0.2) |
| shadow-border-inset | inset 0px 0px 0px 1px rgb(35, 37, 42) |
| shadow-border-outer | 0px 0px 0px 1px rgba(0, 0, 0, 0.2) |

Interaction signals: backdrop-filter blur(4px)/blur(20px); focus outline 3px, colors rgb(247,248,248) / rgb(208,214,224), offset 0.

## Radius Roles

| Token | Px | Role |
|-------|----|------|
| radius-xs | 2 | Hairline corner |
| radius-sm | 4 | Subtle corner (buttons, inputs, chips) |
| radius-md | 6 | Subtle corner (controls, cards) |
| radius-lg | 8 | Control corner |
| radius-xl | 12 | Control corner |
| radius-2xl | 16 | Card corner |
| radius-pill | 9999 | Badges, status chips |

## Do's and Don'ts

| Do | Don't |
|----|-------|
| Maintain consistent spacing using the 4px base grid | Make unsupported claims about absent visual features |
| Maintain WCAG AA contrast ratios (4.5:1 for normal text) | Mix rounded and sharp corners in the same view |
| Use the primary color only for the single most important action per screen | Invent shadows beyond the evidence above |
