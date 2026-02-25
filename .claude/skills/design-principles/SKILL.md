---
name: design-principles
description: Enforce a precise, minimal design system for terminal UIs built with BubbleTea/Lipgloss. Use this skill when building TUI dashboards, CLI tools, or any terminal interface that needs craft — clean, information-dense, every character matters.
---

# TUI Design Principles

This skill enforces precise, crafted design for terminal user interfaces built with Go (BubbleTea, Bubbles, Lipgloss). The philosophy: terminal UIs deserve the same design intentionality as graphical ones. Every cell in the grid matters.

## Design Direction (REQUIRED)

**Before writing any TUI code, commit to a design direction.** Terminals have constraints (fixed-width grid, limited color) but also strengths (speed, density, keyboard-first).

### Think About Context

- **What does this tool do?** A monitoring dashboard needs different density than an interactive wizard.
- **Who uses it?** Sysadmins want density and speed. Developers want clarity and keyboard shortcuts.
- **What's the emotional job?** Confidence? Efficiency? Calm oversight?
- **How long do users stare at it?** Long-running dashboards need muted palettes. Quick CLI tools can be bolder.

### Choose a Personality

**Precision & Density** — Tight layout, monochrome with selective color, information-forward. For power users who live in the terminal. Think `htop`, `lazygit`, `k9s`.

**Utility & Function** — Muted palette, functional density, clear hierarchy. The work matters more than the chrome. Think `tmux` status bars, `git log --oneline`.

**Data & Monitoring** — Table-optimized, numbers as first-class citizens, status colors earned. Think `incus top`, Grafana terminal dashboards.

**Boldness & Clarity** — High contrast, confident use of bold/dim, dramatic negative space. For tools that want to feel modern. Think `charm` CLIs, `glow`.

Pick one. Commit. Let it guide every Lipgloss style decision.

### Choose a Color Strategy

Terminals support 256 colors or true color, but restraint is the craft:

- **Monochrome + one accent** — Most professional. Gray hierarchy with a single color for status/action. Works everywhere.
- **Semantic colors only** — Green=healthy, red=error, yellow=warning. No decorative color.
- **Muted palette** — Desaturated colors that don't fight for attention. Good for long-running dashboards.

**Always respect the terminal's background.** Use `lipgloss.AdaptiveColor` when supporting both light and dark terminals. Default to designing for dark terminals — that's where most users are.

**One accent color.** Pick it and use it consistently for the primary interactive/highlight element. Don't rainbow.

---

## Core Craft Principles

These apply to every TUI. This is the quality floor.

### The Character Grid

Terminals are fixed-width grids. Every character cell is a design decision:

- **1 char** — minimum separator (single space between columns)
- **2 chars** — standard gap (between label and value, column padding)
- **3-4 chars** — generous spacing (section breaks, indentation)

Wasted space in a terminal is more noticeable than in a GUI. Earn every blank cell.

### Column Alignment

**Columns must align perfectly.** This is non-negotiable in a monospace grid:

- Right-align numbers
- Left-align text
- Account for ANSI escape sequences in width calculations — use `lipgloss.Width()`, never `len()`
- Pad content to fixed widths; never let columns shift between refreshes
- Account for cell padding in `bubbles/table` — the `Width` field includes padding, so content gets less space than you specify

### Consistent Padding

Lipgloss `Padding()` and `Margin()` use character cells:

```go
// Good — symmetrical, intentional
style.Padding(0, 1)        // 1 char horizontal padding
style.Margin(1, 0)         // 1 line vertical margin

// Bad — asymmetric without reason
style.Padding(1, 3, 0, 2)
```

Keep it symmetrical unless content demands otherwise.

### Border Strategy

Lipgloss offers several border styles. Pick ONE and use it everywhere:

- `lipgloss.NormalBorder()` — light, unobtrusive (good default)
- `lipgloss.RoundedBorder()` — softer feel
- `lipgloss.ThickBorder()` — bold, heavy (use sparingly)
- No border + dim color — separation through contrast alone

Borders cost 2 characters of width and 2 lines of height. In a dense dashboard, that's expensive. Consider using color contrast or dim separators instead.

### Text Weight Hierarchy

Terminals give you: **bold**, *dim*, normal, and underline. Build a hierarchy:

1. **Bold + bright color** — titles, critical status
2. **Normal + foreground** — primary content, data values
3. **Normal + muted color** — secondary info, labels
4. **Dim** — help text, hints, inactive items

Use all four levels consistently. If everything is bold, nothing is.

### Color for Meaning Only

The default foreground color builds structure. Color only appears when it communicates:

- **Green (82)** — running, healthy, success
- **Red (196)** — stopped, error, critical
- **Yellow (220)** — warning, degraded, pending
- **Blue/Cyan (39/87)** — informational, links, highlights
- **Magenta/Purple (205/57)** — accent, selected, branded

If a color isn't communicating status or guiding attention, remove it. A monochrome table with one colored status column is more readable than a rainbow.

### Selection & Focus

The selected/focused row or item needs clear but not overwhelming treatment:

```go
// Good — subtle background, keeps content readable
SelectedStyle = lipgloss.NewStyle().
    Background(lipgloss.Color("57")).
    Foreground(lipgloss.Color("255"))

// Bad — too many decorations
SelectedStyle = lipgloss.NewStyle().
    Background(lipgloss.Color("57")).
    Foreground(lipgloss.Color("255")).
    Bold(true).
    Underline(true)
```

One treatment (background color shift) is usually enough.

### Responsive Layout

Terminals resize. Handle it:

- Recalculate column widths on `tea.WindowSizeMsg`
- Give one "flex" column (usually NAME) the remaining space
- Set a minimum width for the flex column so it doesn't collapse to nothing
- Set table height to `termHeight - fixedRows` (header, footer, error banner)
- Never hard-code widths that assume a specific terminal size

### Status Bar / Footer

Every TUI benefits from a footer with:

- **Left:** contextual info (last updated, connection status, item count)
- **Right:** keyboard shortcuts (keep it minimal — the 3-5 most common actions)

Use `HelpStyle` (dim/muted) so it doesn't compete with content.

### Error Display

Errors should be visible but not destructive:

- Show as a styled banner (bold red, one line) above or below the main content
- **Never blank the screen** on error — keep stale data visible
- Auto-clear errors when the condition resolves
- Connection errors should trigger retry, not exit

### Spinner & Loading States

- Use `spinner.Dot` or `spinner.Line` — they're calm and professional
- Show what's happening: "Connecting to Incus..." not just a spinner
- Transition cleanly from loading to content (no flicker, no layout shift)

---

## Anti-Patterns

### Never Do This
- Multiple border styles in one interface
- Color on every column (rainbow tables)
- Decorative Unicode box-drawing when simple spaces suffice
- Hard-coded terminal widths
- Blocking the UI on network calls (always use `tea.Cmd`)
- Excessive emoji or Unicode symbols as decoration
- Inconsistent use of bold (either a system or don't use it)

### Always Question
- "Is this color earning its place, or is it decoration?"
- "Will this layout survive a narrow terminal?"
- "Can I remove a visual element without losing meaning?"
- "Does the information hierarchy read correctly in monochrome?"
- "Am I accounting for ANSI widths in column calculations?"

---

## The Standard

Every TUI should feel like it was built by someone who uses terminal tools 8 hours a day and cares about the craft. Not flashy — *precise*. Dense where density serves the user. Minimal where minimalism serves clarity.

The terminal is a canvas of characters. Respect the grid. Earn every color. Make it feel like it belongs next to `htop` and `lazygit`.
