// The CodeMirror text viewer/editor. This module is loaded on demand (see
// TextEditor.svelte) so CodeMirror stays out of the main bundle, and each
// language mode is a further chunk fetched only when a file needs it.

import { Compartment, EditorState, type Extension, type Text } from '@codemirror/state'
import {
  EditorView,
  crosshairCursor,
  drawSelection,
  dropCursor,
  highlightActiveLine,
  highlightActiveLineGutter,
  highlightSpecialChars,
  highlightTrailingWhitespace,
  keymap,
  lineNumbers,
  rectangularSelection,
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import {
  HighlightStyle,
  LanguageDescription,
  bracketMatching,
  foldGutter,
  foldKeymap,
  indentOnInput,
  indentUnit,
  syntaxHighlighting,
} from '@codemirror/language'
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search'
import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete'
import { languages } from '@codemirror/language-data'
import { tags as t } from '@lezer/highlight'

export interface EditorOptions {
  doc: string
  filename: string
  readOnly: boolean
  wrap: boolean
  /** Called on every document or selection change. */
  onupdate?: (info: EditorInfo) => void
}

export interface EditorInfo {
  dirty: boolean
  line: number
  col: number
  /** Number of selected characters, 0 for a plain cursor. */
  selected: number
  lines: number
}

export interface EditorHandle {
  /** The text, with the file's original line endings. */
  getText(): string
  /** Marks the current text as saved (no longer dirty). */
  markSaved(): void
  setReadOnly(readOnly: boolean): void
  setWrap(wrap: boolean): void
  focus(): void
  destroy(): void
  /** The detected language's name, once loaded. */
  language: Promise<string | null>
  /** "Tabs" or "Spaces: N". */
  indent: string
}

/** Chooses the indent unit from the file's own indentation, or `fallback` without any. */
function detectIndent(doc: string, fallback: string): string {
  let tabs = 0
  const spaces = new Map<number, number>()
  let prev = 0
  let n = 0
  for (const line of doc.split('\n', 5000)) {
    const m = /^[ \t]*/.exec(line)![0]
    if (m.length === line.length) continue // blank
    if (m.startsWith('\t')) {
      tabs++
    } else {
      const d = Math.abs(m.length - prev)
      if (d >= 2 && d <= 8) spaces.set(d, (spaces.get(d) ?? 0) + 1)
      prev = m.length
    }
    if (++n > 2000) break
  }
  let best = 0
  let bestCount = 0
  for (const [w, c] of spaces) if (c > bestCount) [best, bestCount] = [w, c]
  if (tabs > bestCount) return '\t'
  if (best) return ' '.repeat(best)
  return tabs ? '\t' : fallback
}

/**
 * The line separator to keep. CodeMirror splits lines on any of \r\n, \r
 * and \n; a file that consistently uses CRLF keeps it, anything else is
 * written with \n (only once the user has edited it).
 */
function detectLineBreak(doc: string): string | undefined {
  const crlf = doc.split('\r\n').length - 1
  if (!crlf) return undefined
  const lf = doc.split('\n').length - 1
  return crlf === lf ? '\r\n' : undefined
}

// Colours come from CSS variables (app.css) so they follow light/dark mode.
const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.modifier, t.controlKeyword, t.definitionKeyword, t.moduleKeyword], color: 'var(--cm-keyword)' },
  { tag: [t.string, t.special(t.string), t.regexp, t.character], color: 'var(--cm-string)' },
  { tag: [t.number, t.bool, t.null, t.atom, t.unit], color: 'var(--cm-number)' },
  { tag: [t.comment, t.lineComment, t.blockComment, t.docComment], color: 'var(--cm-comment)', fontStyle: 'italic' },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.labelName], color: 'var(--cm-function)' },
  { tag: [t.typeName, t.className, t.namespace, t.tagName], color: 'var(--cm-type)' },
  { tag: [t.propertyName, t.attributeName], color: 'var(--cm-property)' },
  { tag: [t.definition(t.variableName), t.constant(t.variableName), t.standard(t.variableName)], color: 'var(--cm-def)' },
  { tag: [t.meta, t.processingInstruction, t.annotation, t.escape], color: 'var(--cm-meta)' },
  { tag: t.heading, color: 'var(--cm-keyword)', fontWeight: 'bold' },
  { tag: t.strong, fontWeight: 'bold' },
  { tag: t.emphasis, fontStyle: 'italic' },
  { tag: t.strikethrough, textDecoration: 'line-through' },
  { tag: [t.link, t.url], color: 'var(--cm-string)', textDecoration: 'underline' },
  { tag: t.invalid, color: 'var(--danger)' },
  { tag: [t.inserted], color: 'var(--ok)' },
  { tag: [t.deleted], color: 'var(--danger)' },
])

const theme = EditorView.theme({
  '&': { height: '100%', fontSize: '12px', backgroundColor: 'var(--bg)', color: 'var(--fg)' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: "ui-monospace, 'SF Mono', Menlo, Consolas, monospace", lineHeight: '1.45' },
  '.cm-gutters': { backgroundColor: 'var(--bg)', color: 'var(--muted)', borderRight: '1px solid var(--border)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--hover)', color: 'var(--fg)' },
  '.cm-activeLine': { backgroundColor: 'var(--cm-active-line)' },
  '.cm-content': { caretColor: 'var(--fg)' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--fg)' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
    { backgroundColor: 'var(--selected) !important' },
  '.cm-selectionMatch': { backgroundColor: 'var(--cm-match)' },
  '.cm-searchMatch': { backgroundColor: 'var(--cm-match)', outline: '1px solid var(--warn)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--cm-match-current)' },
  '&.cm-focused .cm-matchingBracket': { backgroundColor: 'var(--cm-match)', outline: '1px solid var(--muted)' },
  '&.cm-focused .cm-nonmatchingBracket': { color: 'var(--danger)' },
  '.cm-trailingSpace': { backgroundColor: 'var(--cm-trailing)' },
  '.cm-foldPlaceholder': { backgroundColor: 'var(--hover)', border: '1px solid var(--border)', color: 'var(--muted)' },
  '.cm-panels': { backgroundColor: 'var(--hover)', color: 'var(--fg)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border)' },
  '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--border)' },
  '.cm-panel.cm-search': { fontSize: '12px', padding: '4px 8px' },
  '.cm-panel.cm-search input, .cm-panel.cm-search button': { fontSize: '12px', margin: '2px 4px 2px 0' },
  '.cm-panel.cm-search label': { fontSize: '12px', marginRight: '6px' },
  '.cm-textfield': { padding: '2px 6px' },
  '.cm-button': { backgroundImage: 'none', backgroundColor: 'var(--bg)', color: 'var(--fg)', border: '1px solid var(--border)', borderRadius: '4px', padding: '1px 8px' },
  '.cm-panel.cm-search [name=close]': { color: 'var(--muted)' },
  '.cm-tooltip': { backgroundColor: 'var(--bg)', border: '1px solid var(--border)' },
})

/** Finds the language mode for a file name, by extension or name (Dockerfile, nginx.conf…). */
function findLanguage(filename: string): LanguageDescription | null {
  const byName = LanguageDescription.matchFilename(languages, filename)
  if (byName) return byName
  // Common config files the language list doesn't name.
  const lower = filename.toLowerCase()
  const alias: [RegExp, string][] = [
    [/^\.?(env|env\..*|.*\.env)$/, 'Properties files'],
    [/\.(conf|cnf|cfg)$|^\.?(editorconfig|npmrc|gitconfig)$/, 'Properties files'],
    [/^\.?(bashrc|bash_profile|profile|zshrc|bash_aliases)$/, 'Shell'],
    [/\.service$|\.timer$|\.socket$|\.mount$|\.desktop$/, 'Properties files'],
    [/\.(jsonc|json5)$/, 'JSON'],
    [/\.svelte$/, 'HTML'],
    [/\.ya?ml\.dist$/, 'YAML'],
  ]
  for (const [re, name] of alias) {
    if (re.test(lower)) return LanguageDescription.matchLanguageName(languages, name, false)
  }
  return null
}

export function createEditor(parent: HTMLElement, opts: EditorOptions): EditorHandle {
  const unit = detectIndent(opts.doc, /\.ya?ml$/i.test(opts.filename) ? '  ' : '    ')
  const lineBreak = detectLineBreak(opts.doc)
  const readOnly = new Compartment()
  const wrap = new Compartment()
  const lang = new Compartment()
  let saved: Text

  const info = (state: EditorState): EditorInfo => {
    const sel = state.selection.main
    const line = state.doc.lineAt(sel.head)
    return {
      dirty: !state.doc.eq(saved),
      line: line.number,
      col: sel.head - line.from + 1,
      selected: state.selection.ranges.reduce((n, r) => n + r.to - r.from, 0),
      lines: state.doc.lines,
    }
  }

  // Read-only views stay focusable, so the cursor, selection and search still work.
  const ro = (on: boolean): Extension => EditorState.readOnly.of(on)

  const extensions: Extension[] = [
    lineNumbers(),
    highlightActiveLineGutter(),
    highlightSpecialChars(),
    highlightTrailingWhitespace(),
    history(),
    foldGutter(),
    drawSelection(),
    dropCursor(),
    EditorState.allowMultipleSelections.of(true),
    indentOnInput(),
    syntaxHighlighting(highlight, { fallback: true }),
    bracketMatching(),
    closeBrackets(),
    rectangularSelection(),
    crosshairCursor(),
    highlightActiveLine(),
    highlightSelectionMatches(),
    search({ top: true }),
    indentUnit.of(unit),
    EditorState.tabSize.of(unit === '\t' ? 4 : unit.length),
    keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap, indentWithTab]),
    theme,
    readOnly.of(ro(opts.readOnly)),
    wrap.of(opts.wrap ? EditorView.lineWrapping : []),
    lang.of([]),
    EditorView.updateListener.of((u) => {
      if (u.docChanged || u.selectionSet) opts.onupdate?.(info(u.state))
    }),
  ]
  if (lineBreak) extensions.push(EditorState.lineSeparator.of(lineBreak))

  const view = new EditorView({ parent, state: EditorState.create({ doc: opts.doc, extensions }) })
  saved = view.state.doc
  opts.onupdate?.(info(view.state))

  const desc = findLanguage(opts.filename)
  const language = desc
    ? desc.load().then(
        (support) => {
          view.dispatch({ effects: lang.reconfigure(support) })
          return desc.name
        },
        () => null,
      )
    : Promise.resolve(null)

  return {
    getText: () => view.state.sliceDoc(),
    markSaved() {
      saved = view.state.doc
      opts.onupdate?.(info(view.state))
    },
    setReadOnly: (on) => view.dispatch({ effects: readOnly.reconfigure(ro(on)) }),
    setWrap: (on) => view.dispatch({ effects: wrap.reconfigure(on ? EditorView.lineWrapping : []) }),
    focus: () => view.focus(),
    destroy: () => view.destroy(),
    language,
    indent: unit === '\t' ? 'Tabs' : `Spaces: ${unit.length}`,
  }
}
