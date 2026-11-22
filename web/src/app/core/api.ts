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
  /** Usuarios distintos afectados, por id, email o IP. */
  user_count: number;
  first_seen: string;
  last_seen: string;
  last_release: string | null;
  resolved_in_release: string | null;
  last_regression: string | null;
  /** La release del primer evento. */
  first_release?: string | null;
  /** La release cuyo despliegue lo resolvió, si estaba en resolved_next. */
  fixed_in_release?: string | null;
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

export interface TagValue {
  value: string;
  count: number;
  percent: number;
  last_seen: string;
}

export interface TagDistribution {
  key: string;
  total: number;
  distinct: number;
  values: TagValue[];
}

export interface IssueTags {
  issue_id: number;
  events: number;
  user_count: number;
  tags: TagDistribution[];
}

export interface TagKey {
  key: string;
  issues: number;
}

export interface TagValueCount {
  value: string;
  issues: number;
  count: number;
}

/** El error de sintaxis de una búsqueda, como lo da la API con 400. */
export interface QueryError {
  code: string;
  position: number;
  key?: string;
  value?: string;
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

export interface Deploy {
  id: number;
  environment: string;
  deployed_at: string;
}

export interface Release {
  id: number;
  project_id: number;
  project: string;
  version: string;
  first_seen: string;
  finalized_at: string | null;
  deploys: Deploy[];
  new_issues: number;
  issues: number;
  regressions: number;
  events: number;
  last_event: string | null;
}

/** Un issue en el detalle de una release, con sus eventos en ella (o en la anterior, si dejó de salir). */
export interface ReleaseIssue extends Issue {
  release_events: number;
  release_last_seen: string;
}

export interface ReleaseDetail extends Release {
  previous: string | null;
  next: string | null;
  new: ReleaseIssue[];
  continued: ReleaseIssue[];
  regressed: ReleaseIssue[];
  stopped: ReleaseIssue[];
}

export interface Environment {
  name: string;
  issues: number;
  events: number;
  last_seen: string;
}

export interface ReleaseQuery {
  project?: string;
  environment?: string;
  cursor?: string;
  limit?: number;
}

export type ActionType = 'slack' | 'linear' | 'email' | 'discord' | 'telegram' | 'ntfy' | 'webhook';

export interface RuleAction {
  id: string;
  type: ActionType;
  url?: string;
  team_id?: string;
  label_ids?: string[];
  to?: string[];
  bot_token?: string;
  chat_id?: string;
  token?: string;
  secret?: string;
}

export interface RuleConditions {
  new: boolean;
  regressed: boolean;
  frequency: { events: number; window_minutes: number } | null;
  users: { users: number } | null;
  environments: string[];
  levels: string[];
  tags: { key: string; value: string }[];
}

export interface Rule {
  id: number;
  project_id: number;
  name: string;
  enabled: boolean;
  conditions: RuleConditions;
  actions: RuleAction[];
  throttle_minutes: number;
  created_at?: string;
  updated_at?: string;
}

export type RuleInput = Omit<Rule, 'id' | 'project_id' | 'created_at' | 'updated_at'>;

export interface Filters {
  messages: string[];
  exception_types: string[];
  browser_extensions: boolean;
  web_crawlers: boolean;
  localhost: boolean;
  environments: string[];
  releases: string[];
  events_per_issue_per_hour: number | null;
  sample_rate: number;
  rate_limit_per_minute: number | null;
}

export interface DroppedStats {
  since: string;
  total: number;
  reasons: Record<string, number>;
  daily: { date: string; reason: string; count: number }[];
}

export interface IssueQuery {
  project?: string;
  status?: StatusFilter;
  query?: string;
  environment?: string;
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

  /** Sin entorno, todos; con él, los recuentos, la gráfica y el último evento son los suyos. */
  issue(id: number, environment = ''): Observable<IssueDetail> {
    return this.http.get<IssueDetail>(`${BASE}/issues/${id}/`, { params: envParams(environment) });
  }

  events(
    issueId: number,
    limit = 100,
    cursor?: string,
    environment = '',
  ): Observable<Page<EventSummary>> {
    let params = envParams(environment).set('limit', limit);
    if (cursor) {
      params = params.set('cursor', cursor);
    }
    return this.http.get<Page<EventSummary>>(`${BASE}/issues/${issueId}/events/`, { params });
  }

  environments(): Observable<Page<Environment>> {
    return this.http.get<Page<Environment>>(`${BASE}/environments/`);
  }

  releases(q: ReleaseQuery): Observable<Page<Release>> {
    let params = new HttpParams();
    for (const [key, value] of Object.entries(q)) {
      if (value !== undefined && value !== '') {
        params = params.set(key, String(value));
      }
    }
    return this.http.get<Page<Release>>(`${BASE}/releases/`, { params });
  }

  release(projectId: number, version: string, environment = ''): Observable<ReleaseDetail> {
    return this.http.get<ReleaseDetail>(
      `${BASE}/projects/${projectId}/releases/${encodeURIComponent(version)}/`,
      { params: envParams(environment) },
    );
  }

  event(id: number): Observable<StoredEvent> {
    return this.http.get<StoredEvent>(`${BASE}/events/${id}/`);
  }

  issueTags(issueId: number): Observable<IssueTags> {
    return this.http.get<IssueTags>(`${BASE}/issues/${issueId}/tags/`);
  }

  tagKeys(): Observable<Page<TagKey>> {
    return this.http.get<Page<TagKey>>(`${BASE}/tags/`);
  }

  tagValues(key: string, prefix: string): Observable<Page<TagValueCount>> {
    const params = new HttpParams().set('prefix', prefix);
    return this.http.get<Page<TagValueCount>>(`${BASE}/tags/${encodeURIComponent(key)}/values/`, {
      params,
    });
  }

  stacktrace(issueId: number): Observable<string> {
    return this.http.get(`${BASE}/issues/${issueId}/stacktrace.md`, { responseType: 'text' });
  }

  setStatus(issueId: number, status: IssueStatus): Observable<Issue> {
    return this.http.post<Issue>(`${BASE}/issues/${issueId}/status`, { status });
  }

  rules(projectId: number): Observable<Page<Rule>> {
    return this.http.get<Page<Rule>>(`${BASE}/projects/${projectId}/rules`);
  }

  createRule(projectId: number, rule: RuleInput): Observable<Rule> {
    return this.http.post<Rule>(`${BASE}/projects/${projectId}/rules`, rule);
  }

  updateRule(projectId: number, id: number, rule: RuleInput): Observable<Rule> {
    return this.http.put<Rule>(`${BASE}/projects/${projectId}/rules/${id}`, rule);
  }

  deleteRule(projectId: number, id: number): Observable<void> {
    return this.http.delete<void>(`${BASE}/projects/${projectId}/rules/${id}`);
  }

  testRule(projectId: number, id: number, actionId: string): Observable<{ ok: boolean }> {
    return this.http.post<{ ok: boolean }>(`${BASE}/projects/${projectId}/rules/${id}/test`, {
      action_id: actionId,
    });
  }

  filters(projectId: number): Observable<Filters> {
    return this.http.get<Filters>(`${BASE}/projects/${projectId}/filters`);
  }

  setFilters(projectId: number, f: Filters): Observable<Filters> {
    return this.http.patch<Filters>(`${BASE}/projects/${projectId}/filters`, f);
  }

  dropped(projectId: number, days: number): Observable<DroppedStats> {
    return this.http.get<DroppedStats>(`${BASE}/projects/${projectId}/dropped`, {
      params: new HttpParams().set('days', days),
    });
  }
}

function envParams(environment: string): HttpParams {
  return environment ? new HttpParams({ fromObject: { environment } }) : new HttpParams();
}

export function queryError(err: unknown): QueryError | null {
  const e = err as { status?: number; error?: { query_error?: QueryError } };
  return e?.status === 400 && e.error?.query_error ? e.error.query_error : null;
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
