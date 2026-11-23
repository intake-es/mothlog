import type { ActionType, Rule, RuleAction, RuleInput } from '../../core/api';
import { type Key, locale, t } from '../../core/i18n';

export const ACTION_TYPES: ActionType[] = [
  'slack',
  'linear',
  'email',
  'discord',
  'telegram',
  'ntfy',
  'webhook',
  'github',
];

export const ACTION_ICON: Record<ActionType, string> = {
  slack: 'slack-logo',
  linear: 'kanban',
  email: 'envelope-simple',
  discord: 'discord-logo',
  telegram: 'telegram-logo',
  ntfy: 'bell-ringing',
  webhook: 'webhooks-logo',
  github: 'github-logo',
};

export const LEVELS = ['fatal', 'error', 'warning', 'info', 'debug'];

/** Los campos de texto de cada tipo de acción, en el orden en que se piden. */
export type ActionField =
  | 'url'
  | 'repo'
  | 'team_id'
  | 'labels'
  | 'to'
  | 'bot_token'
  | 'chat_id'
  | 'token'
  | 'secret';

export const ACTION_FIELDS: Record<
  ActionType,
  { field: ActionField; label: Key; secret?: boolean }[]
> = {
  slack: [{ field: 'url', label: 'field.slackUrl', secret: true }],
  linear: [
    { field: 'team_id', label: 'field.team' },
    { field: 'labels', label: 'field.labels' },
  ],
  email: [{ field: 'to', label: 'field.to' }],
  discord: [{ field: 'url', label: 'field.discordUrl', secret: true }],
  telegram: [
    { field: 'bot_token', label: 'field.botToken', secret: true },
    { field: 'chat_id', label: 'field.chat' },
  ],
  ntfy: [
    { field: 'url', label: 'field.ntfyUrl', secret: true },
    { field: 'token', label: 'field.ntfyToken', secret: true },
  ],
  webhook: [
    { field: 'url', label: 'field.webhookUrl' },
    { field: 'secret', label: 'field.webhookSecret', secret: true },
  ],
  github: [
    { field: 'repo', label: 'field.githubRepo' },
    { field: 'labels', label: 'field.githubLabels' },
    { field: 'token', label: 'field.githubToken', secret: true },
  ],
};

export const ACTION_HINT: Partial<Record<ActionType, Key>> = {
  webhook: 'field.webhookHint',
  email: 'field.emailHint',
  linear: 'field.linearHint',
  github: 'field.githubHint',
};

export type ActionDraft = { id: string; type: ActionType } & Record<ActionField, string>;

export interface Draft {
  name: string;
  enabled: boolean;
  onNew: boolean;
  onRegressed: boolean;
  frequencyOn: boolean;
  events: number;
  window: number;
  usersOn: boolean;
  users: number;
  cronFailed: boolean;
  cronMissed: boolean;
  cronTimeout: boolean;
  cronRecovered: boolean;
  /** Los slugs separados por comas; vacío, todos. */
  cronMonitors: string;
  uptimeDown: boolean;
  uptimeUp: boolean;
  /** Los slugs separados por comas; vacío, todos. */
  uptimeMonitors: string;
  environments: string;
  levels: string[];
  tags: { key: string; value: string }[];
  actions: ActionDraft[];
  throttle: number;
}

function newID(): string {
  return Math.random().toString(16).slice(2, 10);
}

export function emptyAction(type: ActionType): ActionDraft {
  return {
    id: newID(),
    type,
    url: '',
    repo: '',
    team_id: '',
    labels: '',
    to: '',
    bot_token: '',
    chat_id: '',
    token: '',
    secret: '',
  };
}

export function newDraft(): Draft {
  return {
    name: '',
    enabled: true,
    onNew: true,
    onRegressed: true,
    frequencyOn: false,
    events: 100,
    window: 10,
    usersOn: false,
    users: 10,
    cronFailed: false,
    cronMissed: false,
    cronTimeout: false,
    cronRecovered: false,
    cronMonitors: '',
    uptimeDown: false,
    uptimeUp: false,
    uptimeMonitors: '',
    environments: '',
    levels: [],
    tags: [],
    actions: [emptyAction('webhook')],
    throttle: 30,
  };
}

export function draftOf(r: Rule): Draft {
  const c = r.conditions;
  return {
    name: r.name,
    enabled: r.enabled,
    onNew: c.new,
    onRegressed: c.regressed,
    frequencyOn: !!c.frequency,
    events: c.frequency?.events ?? 100,
    window: c.frequency?.window_minutes ?? 10,
    usersOn: !!c.users,
    users: c.users?.users ?? 10,
    cronFailed: !!c.cron?.failed,
    cronMissed: !!c.cron?.missed,
    cronTimeout: !!c.cron?.timeout,
    cronRecovered: !!c.cron?.recovered,
    cronMonitors: (c.cron?.monitors ?? []).join(', '),
    uptimeDown: !!c.uptime?.down,
    uptimeUp: !!c.uptime?.up,
    uptimeMonitors: (c.uptime?.monitors ?? []).join(', '),
    environments: c.environments.join(', '),
    levels: [...c.levels],
    tags: c.tags.map((tag) => ({ ...tag })),
    actions: r.actions.map((a) => ({
      ...emptyAction(a.type),
      id: a.id,
      url: a.url ?? '',
      team_id: a.team_id ?? '',
      repo: a.repo ?? '',
      labels: ((a.type === 'github' ? a.labels : a.label_ids) ?? []).join(', '),
      to: (a.to ?? []).join(', '),
      bot_token: a.bot_token ?? '',
      chat_id: a.chat_id ?? '',
      token: a.token ?? '',
      secret: a.secret ?? '',
    })),
    throttle: r.throttle_minutes,
  };
}

/** El campo secreto que el servidor no conserva si la acción cambia de URL. */
const BOUND_SECRET: Partial<Record<ActionType, 'token' | 'secret'>> = {
  ntfy: 'token',
  webhook: 'secret',
};

/**
 * Cambia un campo de la acción. Si cambia la URL de ntfy o del webhook, el
 * secreto guardado no la sigue: se vacía para escribirlo otra vez y vuelve si
 * la URL vuelve a ser la guardada.
 */
export function withField(
  a: ActionDraft,
  field: ActionField,
  value: string,
  saved?: RuleAction,
): ActionDraft {
  const next = { ...a, [field]: value };
  const key = BOUND_SECRET[a.type];
  if (field !== 'url' || !key || !saved || saved.type !== a.type) {
    return next;
  }
  const kept = saved[key] ?? '';
  if (value.trim() === (saved.url ?? '')) {
    if (next[key] === '') {
      next[key] = kept;
    }
  } else if (next[key] === kept) {
    next[key] = '';
  }
  return next;
}

/** Si la acción ha cambiado de URL y su secreto guardado hay que escribirlo otra vez. */
export function secretAgain(a: ActionDraft, field: ActionField, saved?: RuleAction): Key | null {
  const key = BOUND_SECRET[a.type];
  if (
    field !== key ||
    !saved ||
    saved.type !== a.type ||
    !saved[key] ||
    a.url.trim() === (saved.url ?? '')
  ) {
    return null;
  }
  return a.type === 'ntfy' ? 'editor.tokenAgain' : 'editor.secretAgain';
}

function list(s: string): string[] {
  return s
    .split(',')
    .map((v) => v.trim())
    .filter(Boolean);
}

export function inputOf(d: Draft): RuleInput {
  return {
    name: d.name.trim(),
    enabled: d.enabled,
    conditions: {
      new: d.onNew,
      regressed: d.onRegressed,
      frequency: d.frequencyOn ? { events: d.events, window_minutes: d.window } : null,
      users: d.usersOn ? { users: d.users } : null,
      cron:
        d.cronFailed || d.cronMissed || d.cronTimeout || d.cronRecovered
          ? {
              failed: d.cronFailed,
              missed: d.cronMissed,
              timeout: d.cronTimeout,
              recovered: d.cronRecovered,
              monitors: list(d.cronMonitors),
            }
          : null,
      uptime:
        d.uptimeDown || d.uptimeUp
          ? { down: d.uptimeDown, up: d.uptimeUp, monitors: list(d.uptimeMonitors) }
          : null,
      environments: list(d.environments),
      levels: d.levels,
      tags: d.tags.filter((tag) => tag.key.trim()),
    },
    actions: d.actions.map((a) => {
      const out: RuleAction = { id: a.id, type: a.type };
      for (const { field } of ACTION_FIELDS[a.type]) {
        if (field === 'labels' && a.type === 'github') {
          out.labels = list(a.labels);
        } else if (field === 'labels') {
          out.label_ids = list(a.labels);
        } else if (field === 'to') {
          out.to = list(a.to);
        } else {
          out[field] = a[field].trim();
        }
      }
      return out;
    }),
    throttle_minutes: d.throttle,
  };
}

/** «10 min», «2 h», «1 h 30 min». */
export function minutes(m: number): string {
  const unit = (n: number, u: 'hour' | 'minute') =>
    new Intl.NumberFormat(locale(), { style: 'unit', unit: u, unitDisplay: 'short' }).format(n);
  if (m < 60) {
    return unit(m, 'minute');
  }
  const h = unit(Math.floor(m / 60), 'hour');
  return m % 60 ? `${h} ${unit(m % 60, 'minute')}` : h;
}

function orList(items: string[]): string {
  return new Intl.ListFormat(locale(), { type: 'disjunction' }).format(items);
}

/** La regla en una línea: «error nuevo o regresión · en production · nivel error». */
export function summary(r: Rule): string {
  const c = r.conditions;
  const nf = new Intl.NumberFormat(locale());
  const when: string[] = [];
  if (c.new) {
    when.push(t('when.new'));
  }
  if (c.regressed) {
    when.push(t('when.regressed'));
  }
  if (c.frequency) {
    when.push(
      t('when.frequency', {
        events: nf.format(c.frequency.events),
        window: minutes(c.frequency.window_minutes),
      }),
    );
  }
  if (c.users) {
    when.push(t('when.users', { users: nf.format(c.users.users) }));
  }
  if (c.cron) {
    const cron = c.cron;
    const kinds: Key[] = [];
    if (cron.failed) {
      kinds.push('when.cronFailed');
    }
    if (cron.missed) {
      kinds.push('when.cronMissed');
    }
    if (cron.timeout) {
      kinds.push('when.cronTimeout');
    }
    if (cron.recovered) {
      kinds.push('when.cronRecovered');
    }
    const text = orList(kinds.map((k) => t(k)));
    when.push(
      cron.monitors.length
        ? `${text} ${t('when.cronMonitors', { list: orList(cron.monitors) })}`
        : text,
    );
  }
  if (c.uptime) {
    const up = c.uptime;
    const kinds: Key[] = [];
    if (up.down) {
      kinds.push('when.uptimeDown');
    }
    if (up.up) {
      kinds.push('when.uptimeUp');
    }
    const text = orList(kinds.map((k) => t(k)));
    when.push(
      up.monitors.length
        ? `${text} ${t('when.cronMonitors', { list: orList(up.monitors) })}`
        : text,
    );
  }
  const parts = [orList(when)];
  if (c.environments.length) {
    parts.push(t('when.environments', { list: orList(c.environments) }));
  }
  if (c.levels.length) {
    parts.push(t('when.levels', { list: orList(c.levels) }));
  }
  for (const tag of c.tags) {
    parts.push(t('when.tag', { tag: tag.value ? `${tag.key}=${tag.value}` : tag.key }));
  }
  const text = parts.join(' · ');
  return text.charAt(0).toLocaleUpperCase(locale()) + text.slice(1);
}

/** Un texto con huecos ({events}) partido en trozos para pintar un campo en cada hueco. */
export function textParts(text: string): { text: string; slot: string }[] {
  const out: { text: string; slot: string }[] = [];
  let last = 0;
  for (const m of text.matchAll(/\{(\w+)\}/g)) {
    out.push({ text: text.slice(last, m.index), slot: m[1] });
    last = (m.index ?? 0) + m[0].length;
  }
  out.push({ text: text.slice(last), slot: '' });
  return out;
}
