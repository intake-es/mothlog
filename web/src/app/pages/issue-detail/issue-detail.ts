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
import { Title } from '@angular/platform-browser';
import { RouterLink } from '@angular/router';
import { firstValueFrom, type Subscription } from 'rxjs';
import {
  Api,
  apiError,
  type EventSummary,
  type IssueDetail,
  type IssueStatus,
  type StoredEvent,
} from '../../core/api';
import { copyText } from '../../core/clipboard';
import { ago, count, fullDate, STATUS_BADGE, STATUS_LABEL, splitTitle } from '../../core/format';
import { DayChart } from '../../ui/day-chart';
import { Icon } from '../../ui/icon';
import { Breadcrumbs } from './breadcrumbs';
import { Stacktrace } from './stacktrace';

interface Action {
  status: IssueStatus;
  label: string;
  icon: string;
  hint?: string;
}

interface Fact {
  label: string;
  value: string;
  title?: string;
  mono?: boolean;
}

const TOP_LABELS: Record<string, string> = {
  release: 'Releases',
  environment: 'Entornos',
  organization_id: 'Espacios',
  user: 'Usuarios',
};

@Component({
  selector: 'app-issue-detail',
  imports: [RouterLink, Icon, DayChart, Stacktrace, Breadcrumbs],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(keydown.escape)': 'menuOpen.set(false)' },
  templateUrl: './issue-detail.html',
  styleUrl: './issue-detail.css',
})
export class IssueDetailPage {
  readonly id = input.required<string>();

  private readonly api = inject(Api);
  private readonly title = inject(Title);

  protected readonly statusLabel = STATUS_LABEL;
  protected readonly statusBadge = STATUS_BADGE;

  protected readonly detail = signal<IssueDetail | null>(null);
  protected readonly loading = signal(true);
  protected readonly error = signal('');
  protected readonly notFound = signal(false);

  protected readonly events = signal<EventSummary[]>([]);
  protected readonly eventsMore = signal(false);
  protected readonly index = signal(0);
  protected readonly event = signal<StoredEvent | null>(null);
  protected readonly eventLoading = signal(false);

  protected readonly busy = signal(false);
  protected readonly menuOpen = signal(false);
  protected readonly copied = signal(false);
  protected readonly toast = signal('');

  private subs: Subscription[] = [];
  private eventSub?: Subscription;
  private timers: ReturnType<typeof setTimeout>[] = [];

  protected readonly heading = computed(() => {
    const d = this.detail();
    return d ? splitTitle(d.title, d.type) : { type: '', message: '' };
  });

  protected readonly total14 = computed(() => {
    const n = (this.detail()?.daily ?? []).reduce((sum, d) => sum + d.count, 0);
    return `${count(n)} ${n === 1 ? 'evento' : 'eventos'}`;
  });

  protected readonly primary = computed<Action | null>(() => {
    const d = this.detail();
    if (!d) {
      return null;
    }
    return d.status === 'open'
      ? { status: 'resolved', label: 'Resolver', icon: 'check-circle' }
      : { status: 'open', label: 'Reabrir', icon: 'arrow-counter-clockwise' };
  });

  protected readonly secondary = computed<Action[]>(() => {
    const d = this.detail();
    if (!d) {
      return [];
    }
    const noRelease = !d.latest_event?.event.release;
    const all: Action[] = [
      { status: 'resolved', label: 'Resolver', icon: 'check-circle' },
      {
        status: 'resolved_next',
        label: 'Resolver en la siguiente versión',
        icon: 'rocket-launch',
        hint: noRelease
          ? 'El último evento no trae release.'
          : `Vuelve a abrirse si llega con otro release que no sea ${d.latest_event?.event.release}.`,
      },
      {
        status: 'silenced',
        label: 'Silenciar',
        icon: 'bell-slash',
        hint: 'Sigue contando, pero no avisa.',
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
      { label: 'Proyecto', value: d.project },
      { label: 'Primera vez', value: ago(d.first_seen), title: fullDate(d.first_seen) },
      { label: 'Última vez', value: ago(d.last_seen), title: fullDate(d.last_seen) },
      { label: 'Eventos', value: count(d.count) },
      { label: 'Release', value: e?.release || '—', mono: !!e?.release },
      { label: 'Entorno', value: e?.environment || '—' },
      {
        label: 'Espacio',
        value: e?.tags?.['organization_id'] || '—',
        mono: !!e?.tags?.['organization_id'],
      },
      { label: 'Usuario', value: user || '—' },
      { label: 'Request', value: req || '—', mono: !!req },
    ];
    if (d.last_regression) {
      facts.splice(3, 0, {
        label: 'Regresión',
        value: ago(d.last_regression),
        title: fullDate(d.last_regression),
      });
    }
    if (d.status === 'resolved_next' && d.resolved_in_release) {
      facts.push({ label: 'Resuelto en', value: d.resolved_in_release, mono: true });
    }
    if (e?.server_name) {
      facts.push({ label: 'Servidor', value: e.server_name, mono: true });
    }
    if (e?.sdk?.name) {
      facts.push({ label: 'SDK', value: `${e.sdk.name} ${e.sdk.version}`, mono: true });
    }
    return facts;
  });

  protected readonly tags = computed(() =>
    Object.entries(this.event()?.event.tags ?? {}).sort(([a], [b]) => a.localeCompare(b)),
  );

  protected readonly top = computed(() => {
    const tv = this.detail()?.top_values ?? {};
    return Object.keys(TOP_LABELS)
      .filter((k) => (tv[k] ?? []).length > 0)
      .map((k) => ({ key: k, label: TOP_LABELS[k], values: tv[k] }));
  });

  protected readonly eventPosition = computed(() => {
    const n = this.events().length;
    return n ? `${count(this.index() + 1)} de ${count(n)}${this.eventsMore() ? '+' : ''}` : '';
  });

  protected readonly eventRows = computed(() =>
    this.events().map((e, i) => ({ e, i, when: fullDate(e.occurred_at), ago: ago(e.occurred_at) })),
  );

  protected readonly count = count;

  protected eventWhen(iso: string): string {
    return `${fullDate(iso)} · ${ago(iso)}`;
  }

  constructor() {
    effect(() => {
      const id = Number(this.id());
      untracked(() => this.load(id));
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

  private load(id: number): void {
    for (const s of this.subs) {
      s.unsubscribe();
    }
    this.loading.set(true);
    this.error.set('');
    this.notFound.set(false);
    this.index.set(0);
    if (!Number.isInteger(id) || id <= 0) {
      this.notFound.set(true);
      this.loading.set(false);
      return;
    }
    this.subs = [
      this.api.issue(id).subscribe({
        next: (d) => {
          this.detail.set(d);
          this.event.set(d.latest_event);
          this.loading.set(false);
          this.title.setTitle(`${d.title} · Errores`);
        },
        error: (err) => {
          if (err?.status === 404) {
            this.notFound.set(true);
          } else {
            this.error.set(apiError(err));
          }
          this.loading.set(false);
        },
      }),
      this.api.events(id).subscribe({
        next: (page) => {
          this.events.set(page.data);
          this.eventsMore.set(!!page.next_cursor);
        },
        error: () => this.events.set([]),
      }),
    ];
  }

  protected retry(): void {
    this.load(Number(this.id()));
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
        this.notify(
          status === 'open' ? 'Reabierto' : `Marcado como ${STATUS_LABEL[status].toLowerCase()}`,
        );
      },
      error: (err) => {
        this.busy.set(false);
        this.notify(apiError(err));
      },
    });
  }

  protected copyForClaude(): void {
    const d = this.detail();
    if (!d) {
      return;
    }
    // La petición sale ya, pero el portapapeles se abre aquí, dentro del clic.
    const md = firstValueFrom(this.api.stacktrace(d.id));
    copyText(md).then(
      () => {
        this.copied.set(true);
        this.notify('Copiado: pégalo en Claude');
        this.later(() => this.copied.set(false), 2000);
      },
      () =>
        md.then(
          () => this.notify('El navegador no ha dejado copiar.'),
          (err) => this.notify(apiError(err)),
        ),
    );
  }

  private notify(message: string): void {
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
