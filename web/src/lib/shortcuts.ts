// Keyboard shortcuts, as shown in the Ctrl+/ guide. Keep in sync with
// onKey in App.svelte.

export const SHORTCUTS: { group: string; keys: [string[], string][] }[] = [
  {
    group: 'Navigation',
    keys: [
      [['Tab'], 'Switch between panes'],
      [['↑', '↓', 'PgUp', 'PgDn', 'Home', 'End'], 'Move the cursor'],
      [['Enter'], 'Open folder, extract archive, or download file'],
      [['Backspace'], 'Go up (or back from search results)'],
      [['Ctrl', 'L'], 'Type a path (Tab completes folder names)'],
      [['A–Z', '0–9', '/'], 'Filter the pane by name (Esc clears)'],
    ],
  },
  {
    group: 'Selection',
    keys: [
      [['Space'], 'Toggle selection of the cursor row'],
      [['Insert'], 'Toggle selection and move down'],
      [['Shift', '↑'], 'Extend the selection (also ↓, PgUp, PgDn, Home, End)'],
      [['Ctrl', 'A'], 'Select all'],
      [['Esc'], 'Clear selection'],
    ],
  },
  {
    group: 'Files',
    keys: [
      [['F2'], 'Rename'],
      [['F3'], 'View'],
      [['Shift', 'F3'], 'Download'],
      [['F4'], 'Edit text file'],
      [['F5'], 'Copy to the other pane'],
      [['F6'], 'Move to the other pane'],
      [['F7'], 'New folder'],
      [['F8', 'Delete'], 'Delete'],
      [['Alt', 'Enter'], 'Properties (ACLs, attributes)'],
    ],
  },
  {
    group: 'Tools',
    keys: [
      [['Alt', 'F7'], 'Find'],
      [['Ctrl', 'U'], 'Disk usage'],
      [['Ctrl', '='], 'Compare panes'],
      [['Ctrl', 'H'], 'Show or hide hidden files'],
      [['Ctrl', 'R'], 'Refresh both panes'],
      [['Ctrl', '/'], 'This guide'],
    ],
  },
  {
    group: 'Editor',
    keys: [[['Ctrl', 'S'], 'Save']],
  },
]
