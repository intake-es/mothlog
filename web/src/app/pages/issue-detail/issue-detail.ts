import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  effect,
  inject,
  input,
  signal,
  untracked,
} from '@angular/core';
import { RouterLink } from '@angular/router';
import { firstValueFrom, type Subscription } from 'rxjs';
import {
  Api,
  apiError,
  type EventSummary,
  type Issue,
  type IssueDetail,
  type IssueLink,
  type IssueStatus,
  type IssueTags,
  type StoredEvent,
} from '../../core/api';
import { copyText } from '../../core/clipboard';
import { environment } from '../../core/environment';
import { ago, count, fullDate, STATUS_BADGE, STATUS_LABEL, splitTitle } from '../../core/format';
import { I18n, type Key, plural } from '../../core/i18n';
import { PageTitle } from '../../core/page-title';
import { DayChart } from '../../ui/day-chart';
import { Icon } from '../../ui/icon';
import { AssigneePicker } from './assignee-picker';
import { Breadcrumbs } from './breadcrumbs';
import { History } from './history';
import { IssueLinks } from './issue-links';
import { LinearCreate } from './linear-create';
import { Stacktrace } from './stacktrace';
import { TagsPanel } from './tags-panel';

interface Action {
  status: IssueStatus;
  label: Key;
  icon: string;
  hint?: string;
}

interface Fact {
  key: Key;
  value: string;
  title?: string;
  mono?: boolean;
  /** Enlace a la release (primera, última y resuelto con) o, con settings, a los ajustes del proyecto. */
  link?: (string | number)[];
  settings?: boolean;
}

const STATUS_TOAST: Record<IssueStatus, Key> = {
  open: 'toast.reopened',
  resolved: 'toast.resolved',
  resolved_next: 'toast.resolved_next',
  silenced: 'toast.silenced',
};

@Component({
  selector: 'app-issue-detail',
  imports: [
    RouterLink,
    Icon,
    DayChart,
    Stacktrace,
    Breadcrumbs,
    TagsPanel,
    History,
    AssigneePicker,
    IssueLinks,
    LinearCreate,
  ],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(keydown.escape)': 'menuOpen.set(false)' },
  templateUrl: './issue-detail.html',
  styleUrl: './issue-detail.css',
})
export class IssueDetailPage {
  readonly id = input.required<string>();

  private readonly api = inject(Api);
  private readonly pageTitle = inject(PageTitle);
  protected readonly t = inject(I18n).t;

  protected readonly statusLabel = STATUS_LABEL;
  protected readonly statusBadge = STATUS_BADGE;

  protected readonly detail = signal<IssueDetail | null>(null);
  protected readonly loading = signal(true);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );
  protected readonly notFound = signal(false);
  protected readonly issueTags = signal<IssueTags | null>(null);

  protected readonly events = signal<EventSummary[]>([]);
  protected readonly eventsMore = signal(false);
  protected readonly index = signal(0);
  protected readonly event = signal<StoredEvent | null>(null);
  protected readonly eventLoading = signal(false);

  protected readonly busy = signal(false);
  protected readonly menuOpen = signal(false);
  protected readonly copied = signal(false);
  protected readonly toast = signal('');
  /** Sube con cada cambio de estado, para que el historial lo vuelva a pedir. */
  protected readonly historyVersion = signal(0);
  /** Si el servidor puede crear issues en Linear (LINEAR_API_KEY). */
  private readonly linearReady = signal(false);
  /** «Crear en Linear» sale con Linear en el servidor y sin issue de Linear en el error. */
  protected readonly canCreateLinear = computed(
    () =>
      this.linearReady() &&
      !(this.detail()?.links ?? []).some((l) => l.provider === 'linear' && l.kind === 'issue'),
  );

  private subs: Subscription[] = [];
  private eventSub?: Subscription;
  private timers: ReturnType<typeof setTimeout>[] = [];

  protected readonly heading = computed(() => {
    const d = this.detail();
    return d ? splitTitle(d.title, d.type) : { type: '', message: '' };
  });

  protected readonly total14 = computed(() => {
    const n = (this.detail()?.daily ?? []).reduce((sum, d) => sum + d.count, 0);
    return plural('events', n);
  });

  protected readonly primary = computed<Action | null>(() => {
    const d = this.detail();
    if (!d) {
      return null;
    }
    return d.status === 'open'
      ? { status: 'resolved', label: 'action.resolve', icon: 'check-circle' }
      : { status: 'open', label: 'action.reopen', icon: 'arrow-counter-clockwise' };
  });

  protected readonly secondary = computed<Action[]>(() => {
    const d = this.detail();
    if (!d) {
      return [];
    }
    const noRelease = !d.latest_event?.event.release;
    const all: Action[] = [
      { status: 'resolved', label: 'action.resolve', icon: 'check-circle' },
      {
        status: 'resolved_next',
        label: 'action.resolveNext',
        icon: 'rocket-launch',
        hint: noRelease
          ? this.t('action.hint.noRelease')
          : this.t('action.hint.next', { release: d.latest_event?.event.release ?? '' }),
      },
      {
        status: 'silenced',
        label: 'action.silence',
        icon: 'bell-slash',
        hint: this.t('action.hint.silence'),
      },
    ];
    return all.filter((a) => a.status !== d.status && a.status !== this.primary()?.status);
  });

  protected readonly noRelease = computed(() => !this.detail()?.latest_event?.event.release);

  protected readonly facts = computed<Fact[]>(() => {
    const d = this.detail();
    if (!d) {
      return [];
    }
    const e = this.event()?.event;
    const u = e?.user;
    const user = u ? u.email || u.username || u.id || u.ip_address || '' : '';
    const req = e?.request ? [e.request.method, e.request.url].filter(Boolean).join(' ') : '';
    const facts: Fact[] = [
      {
        key: 'fact.project',
        value: d.project,
        link: ['/projects', d.project_id, 'settings'],
        settings: true,
      },
      { key: 'fact.firstSeen', value: ago(d.first_seen), title: fullDate(d.first_seen) },
      { key: 'fact.lastSeen', value: ago(d.last_seen), title: fullDate(d.last_seen) },
      { key: 'fact.events', value: count(d.count) },
      { key: 'fact.release', value: e?.release || '—', mono: !!e?.release },
      { key: 'fact.environment', value: e?.environment || '—' },
      {
        key: 'fact.space',
        value: e?.tags?.['organization_id'] || '—',
        mono: !!e?.tags?.['organization_id'],
      },
      { key: 'fact.user', value: user || '—' },
      { key: 'fact.request', value: req || '—', mono: !!req },
    ];
    if (d.last_regression) {
      facts.splice(3, 0, {
        key: 'fact.regression',
        value: ago(d.last_regression),
        title: fullDate(d.last_regression),
      });
    }
    const release = (key: Key, version: string | null | undefined): void => {
      if (version) {
        facts.push({ key, value: version, mono: true, link: ['/releases', d.project_id, version] });
      }
    };
    release('fact.firstRelease', d.first_release);
    release('fact.lastRelease', d.last_release);
    if (d.status === 'resolved_next' && d.resolved_in_release) {
      facts.push({ key: 'fact.resolvedIn', value: d.resolved_in_release, mono: true });
    }
    if (d.status === 'resolved') {
      release('fact.fixedIn', d.fixed_in_release);
    }
    if (e?.server_name) {
      facts.push({ key: 'fact.server', value: e.server_name, mono: true });
    }
    if (e?.sdk?.name) {
      facts.push({ key: 'fact.sdk', value: `${e.sdk.name} ${e.sdk.version}`, mono: true });
    }
    return facts;
  });

  protected readonly tags = computed(() =>
    Object.entries(this.event()?.event.tags ?? {}).sort(([a], [b]) => a.localeCompare(b)),
  );

  protected readonly eventPosition = computed(() => {
    const n = this.events().length;
    return n
      ? this.t('detail.position', {
          i: count(this.index() + 1),
          n: `${count(n)}${this.eventsMore() ? '+' : ''}`,
        })
      : '';
  });

  protected readonly eventRows = computed(() =>
    this.events().map((e, i) => ({ e, i, when: fullDate(e.occurred_at), ago: ago(e.occurred_at) })),
  );

  protected readonly count = count;

  protected eventWhen(iso: string): string {
    return `${fullDate(iso)} · ${ago(iso)}`;
  }

  constructor() {
    this.api.integrations().subscribe({
      next: (s) => this.linearReady.set(!!s?.linear?.token_set),
      error: () => this.linearReady.set(false),
    });
    effect(() => {
      const id = Number(this.id());
      const env = environment();
      untracked(() => this.load(id, env));
    });
    effect(() => {
      const d = this.detail();
      if (d) {
        this.pageTitle.set(() => this.t('title.issueNamed', { title: d.title }));
      }
    });
    inject(DestroyRef).onDestroy(() => {
      for (const s of this.subs) {
        s.unsubscribe();
      }
      this.eventSub?.unsubscribe();
      for (const t of this.timers) {
        clearTimeout(t);
      }
    });
  }

  private load(id: number, env: string): void {
    for (const s of this.subs) {
      s.unsubscribe();
    }
    this.loading.set(true);
    this.failure.set(null);
    this.notFound.set(false);
    this.index.set(0);
    this.issueTags.set(null);
    if (!Number.isInteger(id) || id <= 0) {
      this.notFound.set(true);
      this.loading.set(false);
      return;
    }
    this.subs = [
      this.api.issue(id, env).subscribe({
        next: (d) => {
          this.detail.set(d);
          this.event.set(d.latest_event);
          this.loading.set(false);
        },
        error: (err) => {
          if (err?.status === 404) {
            this.notFound.set(true);
          } else {
            this.failure.set(err);
          }
          this.loading.set(false);
        },
      }),
      this.api.events(id, 100, undefined, env).subscribe({
        next: (page) => {
          this.events.set(page.data);
          this.eventsMore.set(!!page.next_cursor);
        },
        error: () => this.events.set([]),
      }),
      this.api.issueTags(id).subscribe({
        next: (tags) => this.issueTags.set(tags),
        error: () => this.issueTags.set(null),
      }),
    ];
  }

  protected retry(): void {
    this.load(Number(this.id()), environment());
  }

  /** Anterior es el evento más viejo; siguiente, el más nuevo. */
  protected select(i: number): void {
    const list = this.events();
    if (i < 0 || i >= list.length || i === this.index()) {
      return;
    }
    this.index.set(i);
    this.eventSub?.unsubscribe();
    this.eventLoading.set(true);
    this.eventSub = this.api.event(list[i].id).subscribe({
      next: (e) => {
        this.event.set(e);
        this.eventLoading.set(false);
      },
      error: (err) => {
        this.eventLoading.set(false);
        this.notify(apiError(err));
      },
    });
  }

  protected setStatus(status: IssueStatus): void {
    const d = this.detail();
    if (!d || this.busy()) {
      return;
    }
    this.menuOpen.set(false);
    this.busy.set(true);
    this.api.setStatus(d.id, status).subscribe({
      next: (issue) => {
        this.detail.set({ ...d, ...issue });
        this.busy.set(false);
        this.historyVersion.update((v) => v + 1);
        this.notify(this.t(STATUS_TOAST[status]));
      },
      error: (err) => {
        this.busy.set(false);
        this.notify(apiError(err));
      },
    });
  }

  protected onLinearCreated(link: IssueLink): void {
    const d = this.detail();
    if (d) {
      this.detail.set({ ...d, links: [...(d.links ?? []).filter((l) => l.id !== link.id), link] });
      this.historyVersion.update((v) => v + 1);
    }
    this.notify(this.t('linear.created', { identifier: link.identifier || link.external_id }));
  }

  protected onAssigned(issue: Issue): void {
    const d = this.detail();
    if (d) {
      this.detail.set({ ...d, assignee: issue.assignee ?? null });
      this.historyVersion.update((v) => v + 1);
    }
  }

  protected copyForAgent(): void {
    const d = this.detail();
    if (!d) {
      return;
    }
    // La petición sale ya, pero el portapapeles se abre aquí, dentro del clic.
    const md = firstValueFrom(this.api.context(d.id, environment()));
    copyText(md).then(
      () => {
        this.copied.set(true);
        this.notify(this.t('toast.copied'));
        this.later(() => this.copied.set(false), 2000);
      },
      () =>
        md.then(
          () => this.notify(this.t('toast.copyFailed')),
          (err) => this.notify(apiError(err)),
        ),
    );
  }

  protected notify(message: string): void {
    this.toast.set(message);
    this.later(() => {
      if (this.toast() === message) {
        this.toast.set('');
      }
    }, 2600);
  }

  private later(fn: () => void, ms: number): void {
    this.timers.push(setTimeout(fn, ms));
  }
}
