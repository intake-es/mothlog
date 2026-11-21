import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  type ElementRef,
  effect,
  inject,
  input,
  signal,
  untracked,
  viewChild,
} from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import type { Subscription } from 'rxjs';
import {
  Api,
  apiError,
  type Issue,
  type IssueSort,
  type Project,
  type StatusFilter,
} from '../../core/api';
import { ago, count, fullDate, STATUS_BADGE, STATUS_LABEL, splitTitle } from '../../core/format';
import { I18n, type Key, plural } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';
import { Sparkline } from '../../ui/sparkline';

const PAGE = 50;

const STATUS_OPTIONS: { value: StatusFilter; label: Key }[] = [
  { value: 'open', label: 'filter.open' },
  { value: 'resolved', label: 'filter.resolved' },
  { value: 'resolved_next', label: 'filter.resolved_next' },
  { value: 'silenced', label: 'filter.silenced' },
  { value: 'all', label: 'filter.all' },
];

const SORT_OPTIONS: { value: IssueSort; label: Key }[] = [
  { value: 'last_seen', label: 'sort.last_seen' },
  { value: 'first_seen', label: 'sort.first_seen' },
  { value: 'count', label: 'sort.count' },
];

interface Row {
  issue: Issue;
  type: string;
  message: string;
  seen: string;
  seenTitle: string;
  first: string;
  firstTitle: string;
  count: string;
  unit: string;
  regression: boolean;
}

@Component({
  selector: 'app-issue-list',
  imports: [RouterLink, Icon, Pick, Sparkline],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './issue-list.html',
  styleUrl: './issue-list.css',
})
export class IssueListPage {
  // Los filtros viven en la URL (?project=&status=&sort=&query=) para que atrás y un enlace los conserven.
  readonly project = input<string>();
  readonly status = input<string>();
  readonly sort = input<string>();
  readonly query = input<string>();

  private readonly api = inject(Api);
  private readonly router = inject(Router);

  protected readonly t = inject(I18n).t;
  protected readonly statusOptions = computed<PickOption[]>(() =>
    STATUS_OPTIONS.map((o) => ({ value: o.value, label: this.t(o.label) })),
  );
  protected readonly sortOptions = computed<PickOption[]>(() =>
    SORT_OPTIONS.map((o) => ({ value: o.value, label: this.t(o.label) })),
  );
  protected readonly statusLabel = STATUS_LABEL;
  protected readonly statusBadge = STATUS_BADGE;

  protected readonly projects = signal<Project[]>([]);
  protected readonly issues = signal<Issue[]>([]);
  protected readonly nextCursor = signal<string | undefined>(undefined);
  protected readonly loading = signal(true);
  protected readonly loadingMore = signal(false);
  /** El fallo tal cual, para escribir su mensaje en el idioma de cada momento. */
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly searchOpen = signal(false);
  protected readonly searchText = signal('');
  private readonly searchInput = viewChild<ElementRef<HTMLInputElement>>('search');

  protected readonly statusValue = computed<StatusFilter>(() => {
    const s = this.status();
    return STATUS_OPTIONS.some((o) => o.value === s) ? (s as StatusFilter) : 'open';
  });
  protected readonly sortValue = computed<IssueSort>(() => {
    const s = this.sort();
    return SORT_OPTIONS.some((o) => o.value === s) ? (s as IssueSort) : 'last_seen';
  });
  protected readonly projectOptions = computed<PickOption[]>(() => [
    { value: '', label: this.t('list.allProjects') },
    ...this.projects().map((p) => ({ value: p.name, label: p.name })),
  ]);
  protected readonly filtered = computed(
    () => !!this.project() || !!this.query() || this.statusValue() !== 'open',
  );

  protected readonly title = computed(() => {
    if (this.loading() || this.error()) {
      return null;
    }
    return `${count(this.issues().length)}${this.nextCursor() ? '+' : ''}`;
  });

  protected readonly rows = computed<Row[]>(() => {
    const now = Date.now();
    return this.issues().map((issue) => {
      const { type, message } = splitTitle(issue.title, issue.type);
      return {
        issue,
        type,
        message,
        seen: ago(issue.last_seen, now),
        seenTitle: this.t('list.lastSeenAt', { date: fullDate(issue.last_seen) }),
        // La primera vez solo si no es la misma que la última: «hace 1 h · hace 1 h» no dice nada.
        first:
          Math.abs(Date.parse(issue.last_seen) - Date.parse(issue.first_seen)) >= 60_000
            ? this.t('list.firstAgo', { ago: ago(issue.first_seen, now) })
            : '',
        firstTitle: this.t('list.firstSeenAt', { date: fullDate(issue.first_seen) }),
        count: count(issue.count),
        unit: plural('list.unit', issue.count),
        regression: issue.status === 'open' && !!issue.last_regression,
      };
    });
  });

  private sub?: Subscription;
  private searchTimer?: ReturnType<typeof setTimeout>;

  constructor() {
    this.api.projects().subscribe({
      next: (page) => this.projects.set(page.data),
      error: () => this.projects.set([]),
    });

    effect(() => {
      const params = {
        project: this.project() ?? '',
        status: this.statusValue(),
        sort: this.sortValue(),
        query: this.query() ?? '',
      };
      untracked(() => {
        this.searchText.set(params.query);
        if (params.query) {
          this.searchOpen.set(true);
        }
        this.load(params);
      });
    });

    inject(DestroyRef).onDestroy(() => {
      this.sub?.unsubscribe();
      clearTimeout(this.searchTimer);
    });
  }

  private params(): { project: string; status: StatusFilter; sort: IssueSort; query: string } {
    return {
      project: this.project() ?? '',
      status: this.statusValue(),
      sort: this.sortValue(),
      query: this.query() ?? '',
    };
  }

  private load(params: ReturnType<IssueListPage['params']>): void {
    this.sub?.unsubscribe();
    this.loading.set(true);
    this.failure.set(null);
    this.sub = this.api.issues({ ...params, limit: PAGE }).subscribe({
      next: (page) => {
        this.issues.set(page.data);
        this.nextCursor.set(page.next_cursor);
        this.loading.set(false);
      },
      error: (err) => {
        this.failure.set(err);
        this.loading.set(false);
      },
    });
  }

  protected retry(): void {
    this.load(this.params());
  }

  protected more(): void {
    const cursor = this.nextCursor();
    if (!cursor || this.loadingMore()) {
      return;
    }
    this.loadingMore.set(true);
    this.api.issues({ ...this.params(), limit: PAGE, cursor }).subscribe({
      next: (page) => {
        this.issues.update((list) => [...list, ...page.data]);
        this.nextCursor.set(page.next_cursor);
        this.loadingMore.set(false);
      },
      error: (err) => {
        this.failure.set(err);
        this.loadingMore.set(false);
      },
    });
  }

  protected setFilter(key: 'project' | 'status' | 'sort' | 'query', value: string): void {
    const defaults: Record<string, string> = {
      project: '',
      status: 'open',
      sort: 'last_seen',
      query: '',
    };
    void this.router.navigate([], {
      queryParams: { [key]: value === defaults[key] ? null : value },
      queryParamsHandling: 'merge',
      replaceUrl: key === 'query',
    });
  }

  protected openSearch(): void {
    this.searchOpen.set(true);
    setTimeout(() => this.searchInput()?.nativeElement.focus());
  }

  protected onSearch(value: string): void {
    this.searchText.set(value);
    clearTimeout(this.searchTimer);
    this.searchTimer = setTimeout(() => this.setFilter('query', value.trim()), 250);
  }

  protected closeSearch(): void {
    clearTimeout(this.searchTimer);
    this.searchText.set('');
    this.searchOpen.set(false);
    if (this.query()) {
      this.setFilter('query', '');
    }
  }

  protected clearFilters(): void {
    void this.router.navigate([], { queryParams: {} });
  }
}
