// File tools: viewers and editors for particular kinds of files, offered as
// an "Open with" action and on Enter. To add one, write a component taking
// ToolProps and list it in TOOLS. A tool with `server: true` uses the
// server-side tool of the same id (internal/api/tools.go), takes its
// extensions from there, and is hidden if the server doesn't have it.

import type { Component } from 'svelte'
import type { Entry, ServerTool } from '../api'
import SqliteTool from './SqliteTool.svelte'

/** Props every tool component receives. */
export interface ToolProps {
  entry: Entry
  onclose: () => void
  /** Reports an outcome in the status bar. */
  ondone: (msg: string) => void
}

export interface FileTool {
  id: string
  /** Shown in "Open with" and the dialog title. */
  name: string
  /** Lower-case extensions without the dot; server tools get the server's list. */
  extensions?: string[]
  component: Component<ToolProps>
  /** Needs the server tool with the same id. */
  server?: boolean
  /** Open matching files with this tool on Enter / double-click. */
  openOnEnter?: boolean
}

const TOOLS: FileTool[] = [{ id: 'sqlite', name: 'SQLite', component: SqliteTool, server: true, openOnEnter: true }]

/** The tools available for a file, given the server's tools from /api/v1/info. */
export function toolsFor(entry: Entry, serverTools: ServerTool[]): FileTool[] {
  if (entry.type !== 'file') return []
  const dot = entry.name.lastIndexOf('.')
  if (dot <= 0) return []
  const ext = entry.name.slice(dot + 1).toLowerCase()
  return TOOLS.filter((t) => {
    const srv = serverTools.find((s) => s.id === t.id)
    if (t.server && !srv) return false
    return (t.extensions ?? srv?.extensions ?? []).includes(ext)
  })
}
