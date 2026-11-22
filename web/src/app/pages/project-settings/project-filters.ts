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
import { Api, apiError, type DroppedStats, type Filters } from '../../core/api';
import { count } from '../../core/format';
import { I18n, type Key, plural } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';

/** El tope por issue y hora que usa el servidor si el proyecto no pone el suyo. */
const DEFAULT_PER_ISSUE = 100;
const PERIODS = [7, 30, 90];

type ListField = 'messages' | 'exception_types' | 'environments' | 'releases';
type Toggle = 'browser_extensions' | 'web_crawlers' | 'localhost';

const LISTS: { field: ListField; label: Key; hint: Key }[] = [
  { field: 'messages', label: 'filters.messages', hint: 'filters.messagesHint' },
  { field: 'exception_types', label: 'filters.exceptionTypes', hint: 'filters.exceptionTypesHint' },
  { field: 'environments', label: 'filters.environments', hint: 'filters.lines' },
  { field: 'releases', label: 'filters.releases', hint: 'filters.releasesHint' },
];

const TOGGLES: { field: Toggle; label: Key }[] = [
  { field: 'browser_extensions', label: 'filters.browserExtensions' },
  { field: 'web_crawlers', label: 'filters.webCrawlers' },
  { field: 'localhost', label: 'filters.localhost' },
];

interface FilterDraft {
  lists: Record<ListField, string>;
  toggles: Record<Toggle, boolean>;
  perIssue: string;
  sample: string;
  rateLimit: string;
}

function draftOf(f: Filters): FilterDraft {
  return {
    lists: {
      messages: f.messages.join('\n'),
      exception_types: f.exception_types.join('\n'),
      environments: f.environments.join('\n'),
      releases: f.releases.join('\n'),
    },
    toggles: {
      browser_extensions: f.browser_extensions,
      web_crawlers: f.web_crawlers,
      localhost: f.localhost,
    },
    perIssue: f.events_per_issue_per_hour === null ? '' : String(f.events_per_issue_per_hour),
    sample: String(Math.round(f.sample_rate * 1000) / 10),
    rateLimit: f.rate_limit_per_minute === null ? '' : String(f.rate_limit_per_minute),
  };
}

function lines(s: string, keepSpaces: boolean): string[] {
  return s
    .split('\n')
    .map((v) => (keepSpaces ? v : v.trim()))
    .filter((v) => v.trim());
}

function optionalInt(s: string): number | null {
  return s.trim() === '' ? null : Number.parseInt(s, 10);
}

function filtersOf(d: FilterDraft): Filters {
  return {
    messages: lines(d.lists.messages, true),
    exception_types: lines(d.lists.exception_types, false),
    environments: lines(d.lists.environments, false),
    releases: lines(d.lists.releases, false),
    ...d.toggles,
    events_per_issue_per_hour: optionalInt(d.perIssue),
    sample_rate: Math.min(1, Math.max(0, (Number.parseFloat(d.sample) || 0) / 100)),
    rate_limit_per_minute: optionalInt(d.rateLimit),
  };
}

/** La sección Filtros: lo que se descarta, el tope, el muestreo, el límite y lo descartado. */
@Component({
  selector: 'app-project-filters',
  imports: [Icon, Pick],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './project-filters.html',
  styleUrls: ['./settings.css', './project-filters.css'],
})
export class ProjectFilters {
  readonly projectId = input.required<number>();

  private readonly api = inject(Api);
  protected readonly i18n = inject(I18n);
  protected readonly t = this.i18n.t;
  protected readonly lists = LISTS;
  protected readonly toggles = TOGGLES;
  protected readonly defaultPerIssue = DEFAULT_PER_ISSUE;

  protected readonly draft = signal<FilterDraft | null>(null);
  private readonly initial = signal('');
  protected readonly dirty = computed(() => JSON.stringify(this.draft()) !== this.initial());
  protected readonly busy = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );
  private readonly saveFailure = signal<unknown>(null);
  protected readonly saveError = computed(() =>
    this.saveFailure() === null ? '' : apiError(this.saveFailure()),
  );
  protected readonly toast = signal('');
  private toastTimer?: ReturnType<typeof setTimeout>;

  protected readonly days = signal(30);
  protected readonly dropped = signal<DroppedStats | null>(null);
  protected readonly periodOptions = computed<PickOption[]>(() =>
    PERIODS.map((n) => ({ value: String(n), label: this.t('dropped.days', { n }) })),
  );
  protected readonly droppedRows = computed(() => {
    this.i18n.lang();
    const st = this.dropped();
    if (!st) {
      return [];
    }
    const max = Math.max(1, ...Object.values(st.reasons));
    return Object.entries(st.reasons)
      .filter(([, n]) => n > 0)
      .sort((a, b) => b[1] - a[1])
      .map(([reason, n]) => ({
        reason,
        label: this.t(`dropped.reason.${reason}` as Key),
        count: count(n),
        width: `${Math.max(2, (n / max) * 100)}%`,
      }));
  });
  protected readonly droppedTotal = computed(() => {
    const st = this.dropped();
    return st ? plural('dropped.total', st.total, { days: this.days() }) : '';
  });

  constructor() {
    effect(() => {
      const id = this.projectId();
      untracked(() => this.load(id));
    });
    inject(DestroyRef).onDestroy(() => clearTimeout(this.toastTimer));
  }

  protected load(id = this.projectId()): void {
    this.failure.set(null);
    this.draft.set(null);
    this.api.filters(id).subscribe({
      next: (f) => this.reset(f),
      error: (err) => this.failure.set(err),
    });
    this.loadDropped(id);
  }

  private loadDropped(id = this.projectId()): void {
    this.dropped.set(null);
    this.api.dropped(id, this.days()).subscribe({
      next: (st) => this.dropped.set(st),
      error: () => this.dropped.set(null),
    });
  }

  protected setDays(value: string): void {
    this.days.set(Number(value));
    this.loadDropped();
  }

  private reset(f: Filters): void {
    const d = draftOf(f);
    this.draft.set(d);
    this.initial.set(JSON.stringify(d));
  }

  protected setList(field: ListField, value: string): void {
    this.draft.update((d) => (d ? { ...d, lists: { ...d.lists, [field]: value } } : d));
  }

  protected setToggle(field: Toggle, value: boolean): void {
    this.draft.update((d) => (d ? { ...d, toggles: { ...d.toggles, [field]: value } } : d));
  }

  protected set(field: 'perIssue' | 'sample' | 'rateLimit', value: string): void {
    this.draft.update((d) => (d ? { ...d, [field]: value } : d));
  }

  protected save(): void {
    const d = this.draft();
    if (!d || this.busy()) {
      return;
    }
    this.busy.set(true);
    this.saveFailure.set(null);
    this.api.setFilters(this.projectId(), filtersOf(d)).subscribe({
      next: (f) => {
        this.busy.set(false);
        this.reset(f);
        clearTimeout(this.toastTimer);
        this.toast.set(this.t('filters.saved'));
        this.toastTimer = setTimeout(() => this.toast.set(''), 2500);
      },
      error: (err) => {
        this.busy.set(false);
        this.saveFailure.set(err);
      },
    });
  }
}
