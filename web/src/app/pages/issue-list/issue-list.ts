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
import { Router, RouterLink } from '@angular/router';
import type { Subscription } from 'rxjs';
import {
  Api,
  apiError,
  type Issue,
  type IssueSort,
  type Project,
  queryError,
} from '../../core/api';
import { environment } from '../../core/environment';
import { ago, count, fullDate, STATUS_BADGE, STATUS_LABEL, splitTitle } from '../../core/format';
import { I18n, type Key, plural } from '../../core/i18n';
import { DEFAULT_QUERY, MINE, type SearchStatus, setTerm, termValue } from '../../core/search';
import { Avatar } from '../../ui/avatar';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';
import { Sparkline } from '../../ui/sparkline';
import { SearchBar } from './search-bar';

const PAGE = 50;

const STATUS_OPTIONS: { value: SearchStatus | 'all'; label: Key }[] = [
  { value: 'open', label: 'filter.open' },
  { value: 'regressed', label: 'filter.regressed' },
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
  users: string;
  regression: boolean;
  /** «Asignado a Ana», para el title y el lector de pantalla. */
  assignee: string;
}

@Component({
  selector: 'app-issue-list',
  imports: [RouterLink, Avatar, Icon, Pick, Sparkline, SearchBar],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './issue-list.html',
  styleUrl: './issue-list.css',
})
export class IssueListPage {
  // La búsqueda y el orden viven en la URL (?query=&sort=) para que atrás y un enlace los conserven.
  // Sin query, la búsqueda es DEFAULT_QUERY; project y status son los de los enlaces de antes.
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
  protected readonly badQuery = computed(() => queryError(this.failure()));

  /** La búsqueda que se aplica: la de la URL con los filtros de los enlaces de antes dentro. */
  protected readonly search = computed(() => {
    let q = this.query() ?? DEFAULT_QUERY;
    const status = this.status();
    if (status) {
      q = setTerm(q, 'is', status === 'all' ? null : status);
    }
    const project = this.project();
    if (project) {
      q = setTerm(q, 'project', project);
    }
    return q;
  });

  protected readonly statusValue = computed(() => {
    const s = termValue(this.search(), 'is') ?? 'all';
    return STATUS_OPTIONS.some((o) => o.value === s) ? s : '';
  });
  protected readonly mine = computed(() => termValue(this.search(), 'assigned') === MINE);
  protected readonly projectValue = computed(() => termValue(this.search(), 'project') ?? '');
  protected readonly projectNames = computed(() => this.projects().map((p) => p.name));
  protected readonly sortValue = computed<IssueSort>(() => {
    const s = this.sort();
    return SORT_OPTIONS.some((o) => o.value === s) ? (s as IssueSort) : 'last_seen';
  });
  protected readonly projectOptions = computed<PickOption[]>(() => [
    { value: '', label: this.t('list.allProjects') },
    ...this.projects().map((p) => ({ value: p.name, label: p.name })),
  ]);
  protected readonly filtered = computed(() => this.search().trim() !== DEFAULT_QUERY);

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
        users: issue.user_count > 0 ? plural('list.users', issue.user_count) : '',
        regression: issue.status === 'open' && !!issue.last_regression,
        assignee: issue.assignee
          ? this.t(issue.assignee.active ? 'list.assignedTo' : 'list.assignedToRevoked', {
              name: issue.assignee.display_name || issue.assignee.name,
            })
          : '',
      };
    });
  });

  private sub?: Subscription;

  constructor() {
    this.api.projects().subscribe({
      next: (page) => this.projects.set(page.data),
      error: () => this.projects.set([]),
    });

    effect(() => {
      const params = this.params();
      untracked(() => this.load(params));
    });

    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  /** El estado va en la búsqueda (is:), así que a la API se le pide con status=all. */
  private params(): { status: 'all'; sort: IssueSort; query: string; environment: string } {
    return {
      status: 'all',
      sort: this.sortValue(),
      query: this.search(),
      environment: environment(),
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

  protected setSort(value: string): void {
    void this.router.navigate([], {
      queryParams: { sort: value === 'last_seen' ? null : value },
      queryParamsHandling: 'merge',
    });
  }

  /** Los filtros en píldora escriben su término en la búsqueda. */
  protected setStatus(value: string): void {
    this.setQuery(setTerm(this.search(), 'is', value === 'all' ? null : value));
  }

  protected toggleMine(): void {
    this.setQuery(setTerm(this.search(), 'assigned', this.mine() ? null : MINE));
  }

  protected setProject(value: string): void {
    this.setQuery(setTerm(this.search(), 'project', value || null));
  }

  protected setQuery(q: string): void {
    void this.router.navigate([], {
      queryParams: { query: q === DEFAULT_QUERY ? null : q, status: null, project: null },
      queryParamsHandling: 'merge',
    });
  }

  protected clearFilters(): void {
    void this.router.navigate([], { queryParams: {} });
  }
}
