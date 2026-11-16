// Client side of the log file tool (internal/logview).

import { api } from '../api'

export type LogLevel = 'error' | 'warn' | 'info' | 'debug' | 'trace'

export interface LogLine {
  /** Byte offset of the line in the file. */
  o: number
  t: string
  l?: LogLevel
  /** Cut to the server's line length limit. */
  cut?: boolean
  /** The last line, not yet ended by a newline. */
  partial?: boolean
}

export interface LogFilter {
  text?: string
  regex?: boolean
  case?: boolean
  /** Keep the lines that don't match text. */
  not?: boolean
  /** Keep lines at this level or more severe. */
  level?: LogLevel | ''
}

export interface LogPage {
  lines: LogLine[]
  /** Offset of the first line examined; read earlier lines before it. 0 at the start of the file. */
  start: number
  /** Offset after the last complete line; follow from here. */
  end: number
  more?: boolean
  size: number
  inode: number
  /** The file was truncated or replaced since it was read. */
  reset?: boolean
}

export const logFile = (path: string) => ({
  tail: (args: { before?: number; lines?: number; filter?: LogFilter }) => api.tool<LogPage>('log', 'tail', path, args),
  follow: (args: { after: number; inode: number; lines?: number; filter?: LogFilter }) =>
    api.tool<LogPage>('log', 'follow', path, args),
})
