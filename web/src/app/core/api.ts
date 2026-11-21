import { HttpClient, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import type { Observable } from 'rxjs';
import { t } from './i18n';

export type IssueStatus = 'open' | 'resolved' | 'resolved_next' | 'silenced';
export type StatusFilter = IssueStatus | 'all';
export type IssueSort = 'last_seen' | 'first_seen' | 'count';

export interface DayCount {
  date: string;
  count: number;
}

export interface ValueCount {
  value: string;
  count: number;
}

export interface Issue {
  id: number;
  project_id: number;
  project: string;
  title: string;
  culprit: string;
  type: string;
  level: string;
  status: IssueStatus;
  count: number;
  first_seen: string;
  last_seen: string;
  last_release: string | null;
  resolved_in_release: string | null;
  last_regression: string | null;
  daily?: DayCount[];
}

export interface Frame {
  filename?: string;
  abs_path?: string;
  function?: string;
  module?: string;
  lineno?: number;
  colno?: number;
  in_app?: boolean;
  pre_context?: string[];
  context_line?: string;
  post_context?: string[];
  minified?: { filename: string; lineno: number; colno: number };
}

export interface ExceptionValue {
  type?: string;
  value?: string;
  module?: string;
  mechanism?: { type?: string; handled?: boolean };
  frames?: Frame[];
}

export interface SentryEvent {
  event_id: string;
  timestamp: string;
  platform?: string;
  level: string;
  logger?: string;
  release?: string;
  environment?: string;
  sdk: { name: string; version: string };
  message?: string;
  exceptions?: ExceptionValue[];
  tags?: Record<string, string>;
  user?: { id?: string; email?: string; username?: string; ip_address?: string };
  request?: { method?: string; url?: string };
  transaction?: string;
  server_name?: string;
  breadcrumbs?: unknown;
  contexts?: unknown;
}

export interface StoredEvent {
  id: number;
  issue_id: number;
  project_id: number;
  event_id: string;
  occurred_at: string;
  received_at: string;
  event: SentryEvent;
}

export interface IssueDetail extends Issue {
  latest_event: StoredEvent | null;
  daily: DayCount[];
  top_values: Record<string, ValueCount[]>;
}

export interface EventSummary {
  id: number;
  event_id: string;
  occurred_at: string;
  received_at: string;
  release: string;
  environment: string;
  user: string;
  message: string;
}

export interface Page<T> {
  data: T[];
  next_cursor?: string;
}

export interface Project {
  id: number;
  name: string;
  dsn: string;
}

export interface IssueQuery {
  project?: string;
  status?: StatusFilter;
  query?: string;
  sort?: IssueSort;
  cursor?: string;
  limit?: number;
}

const BASE = '/api/0';

@Injectable({ providedIn: 'root' })
export class Api {
  private readonly http = inject(HttpClient);

  projects(): Observable<Page<Project>> {
    return this.http.get<Page<Project>>(`${BASE}/projects/`);
  }

  /** La lista siempre pide la serie diaria, para la minigráfica de cada fila. */
  issues(q: IssueQuery): Observable<Page<Issue>> {
    let params = new HttpParams().set('daily', '1');
    for (const [key, value] of Object.entries(q)) {
      if (value !== undefined && value !== '') {
        params = params.set(key, String(value));
      }
    }
    return this.http.get<Page<Issue>>(`${BASE}/issues/`, { params });
  }

  issue(id: number): Observable<IssueDetail> {
    return this.http.get<IssueDetail>(`${BASE}/issues/${id}/`);
  }

  events(issueId: number, limit = 100, cursor?: string): Observable<Page<EventSummary>> {
    let params = new HttpParams().set('limit', limit);
    if (cursor) {
      params = params.set('cursor', cursor);
    }
    return this.http.get<Page<EventSummary>>(`${BASE}/issues/${issueId}/events/`, { params });
  }

  event(id: number): Observable<StoredEvent> {
    return this.http.get<StoredEvent>(`${BASE}/events/${id}/`);
  }

  stacktrace(issueId: number): Observable<string> {
    return this.http.get(`${BASE}/issues/${issueId}/stacktrace.md`, { responseType: 'text' });
  }

  setStatus(issueId: number, status: IssueStatus): Observable<Issue> {
    return this.http.post<Issue>(`${BASE}/issues/${issueId}/status`, { status });
  }
}

/** El mensaje de un error de la API, que responde {"error": "..."} y se pinta tal cual. */
export function apiError(err: unknown): string {
  const e = err as { status?: number; error?: { error?: string } | string };
  if (e?.error && typeof e.error === 'object' && e.error.error) {
    return e.error.error;
  }
  // Las peticiones de texto (stacktrace.md) traen el JSON del error sin leer.
  if (typeof e?.error === 'string') {
    try {
      const body = JSON.parse(e.error) as { error?: unknown };
      if (typeof body?.error === 'string' && body.error) {
        return body.error;
      }
    } catch {
      // No era JSON.
    }
  }
  if (e?.status === 0) {
    return t('api.offline');
  }
  return t('api.failed');
}
