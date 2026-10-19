import { SvelteMap } from 'svelte/reactivity'
import type { Job, JobEvent, JobState } from './api'

export const isFinished = (s: JobState) => s === 'done' || s === 'failed' || s === 'cancelled'

interface Handlers {
  /** Directories changed; null means "anything may have changed" (reconnect). */
  onchanged: (dirs: string[] | null) => void
  onfinished: (job: Job) => void
}

/** Live job list fed by the /api/v1/ws WebSocket, reconnecting as needed. */
export class JobsStore {
  jobs = $state<Job[]>([])
  connected = $state(false)
  /** Smoothed transfer rate in bytes/second per running job. */
  rates = new SvelteMap<string, number>()

  active = $derived(this.jobs.filter((j) => !isFinished(j.state)).length)

  #handlers: Handlers
  #ws: WebSocket | null = null
  #retry = 1000
  #timer: ReturnType<typeof setTimeout> | undefined
  #samples = new Map<string, { bytes: number; t: number }>()
  #closed = false
  #waiters = new Map<string, ((job: Job) => void)[]>()

  constructor(handlers: Handlers) {
    this.#handlers = handlers
  }

  connect() {
    this.#closed = false
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/api/v1/ws`)
    this.#ws = ws
    ws.onopen = () => {
      this.connected = true
      this.#retry = 1000
    }
    ws.onmessage = (m) => this.#handle(JSON.parse(m.data) as JobEvent)
    ws.onclose = () => {
      this.connected = false
      if (this.#closed) return
      this.#timer = setTimeout(() => this.connect(), this.#retry)
      this.#retry = Math.min(this.#retry * 2, 15000)
    }
  }

  /** Resolves once the job has finished (in any state). */
  waitFor(id: string): Promise<Job> {
    const known = this.jobs.find((j) => j.id === id)
    if (known && isFinished(known.state)) return Promise.resolve(known)
    return new Promise((resolve) => {
      this.#waiters.set(id, [...(this.#waiters.get(id) ?? []), resolve])
    })
  }

  close() {
    this.#closed = true
    clearTimeout(this.#timer)
    this.#ws?.close()
  }

  #handle(ev: JobEvent) {
    switch (ev.type) {
      case 'snapshot':
        this.jobs = ev.jobs
        for (const j of ev.jobs) {
          if (!isFinished(j.state)) continue
          for (const resolve of this.#waiters.get(j.id) ?? []) resolve(j)
          this.#waiters.delete(j.id)
        }
        this.#handlers.onchanged(null)
        break
      case 'job': {
        const i = this.jobs.findIndex((j) => j.id === ev.job.id)
        const prev = i >= 0 ? this.jobs[i] : undefined
        if (i >= 0) this.jobs[i] = ev.job
        else this.jobs.push(ev.job)
        this.#sample(ev.job)
        if (isFinished(ev.job.state)) {
          if (prev && !isFinished(prev.state)) this.#handlers.onfinished(ev.job)
          for (const resolve of this.#waiters.get(ev.job.id) ?? []) resolve(ev.job)
          this.#waiters.delete(ev.job.id)
        }
        break
      }
      case 'removed':
        this.jobs = this.jobs.filter((j) => j.id !== ev.id)
        this.rates.delete(ev.id)
        break
      case 'changed':
        this.#handlers.onchanged(ev.dirs ?? [])
        break
    }
  }

  /** Updates the exponentially smoothed byte rate of a running job. */
  #sample(job: Job) {
    if (job.state !== 'running') {
      this.#samples.delete(job.id)
      this.rates.delete(job.id)
      return
    }
    const now = performance.now()
    const prev = this.#samples.get(job.id)
    this.#samples.set(job.id, { bytes: job.progress.bytes, t: now })
    if (!prev || now <= prev.t) return
    const rate = ((job.progress.bytes - prev.bytes) * 1000) / (now - prev.t)
    const old = this.rates.get(job.id)
    this.rates.set(job.id, old === undefined ? rate : old * 0.7 + rate * 0.3)
  }
}
