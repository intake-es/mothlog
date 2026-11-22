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
import { Api, apiError, type Project, type Release } from '../../core/api';
import { environment } from '../../core/environment';
import { ago, count, fullDate, type HealthTone, healthTone, percent } from '../../core/format';
import { I18n, plural, t } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';

const PAGE = 50;

interface DeployChip {
  environment: string;
  ago: string;
  title: string;
}

interface Row {
  release: Release;
  link: (string | number)[];
  seen: string;
  seenTitle: string;
  deploys: DeployChip[];
  news: string;
  regressions: string;
  events: string;
  health: HealthCell;
}

interface HealthCell {
  none: boolean;
  rate: string;
  tone: HealthTone;
  adoption: string;
  title: string;
}

/** Las releases de la más nueva a la más vieja, con lo que trajo cada una. */
@Component({
  selector: 'app-release-list',
  imports: [RouterLink, Icon, Pick],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './release-list.html',
  styleUrl: './release-list.css',
})
export class ReleaseListPage {
  /** ?project= en la URL, como en la lista de errores. */
  readonly project = input<string>();

  private readonly api = inject(Api);
  private readonly router = inject(Router);
  protected readonly t = inject(I18n).t;
  protected readonly env = environment;

  protected readonly projects = signal<Project[]>([]);
  protected readonly releases = signal<Release[]>([]);
  protected readonly nextCursor = signal<string | undefined>(undefined);
  protected readonly loading = signal(true);
  protected readonly loadingMore = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly projectOptions = computed<PickOption[]>(() => [
    { value: '', label: this.t('list.allProjects') },
    ...this.projects().map((p) => ({ value: p.name, label: p.name })),
  ]);
  protected readonly filtered = computed(() => !!this.project() || !!this.env());

  protected readonly title = computed(() =>
    this.loading() || this.error()
      ? null
      : `${count(this.releases().length)}${this.nextCursor() ? '+' : ''}`,
  );

  protected readonly rows = computed<Row[]>(() => {
    const now = Date.now();
    return this.releases().map((release) => ({
      release,
      link: ['/releases', release.project_id, release.version],
      seen: ago(release.first_seen, now),
      seenTitle: this.t('releases.firstSeenAt', { date: fullDate(release.first_seen) }),
      deploys: latestPerEnvironment(release).map((d) => ({
        environment: d.environment,
        ago: ago(d.deployed_at, now),
        title: this.t('releases.deployedAt', { env: d.environment, date: fullDate(d.deployed_at) }),
      })),
      news: release.new_issues
        ? plural('releases.new', release.new_issues)
        : this.t('releases.noNew'),
      regressions: plural('releases.regressions', release.regressions),
      events: count(release.events),
      health: healthCell(release),
    }));
  });

  private sub?: Subscription;

  constructor() {
    this.api.projects().subscribe({
      next: (page) => this.projects.set(page.data),
      error: () => this.projects.set([]),
    });
    effect(() => {
      const q = { project: this.project() ?? '', environment: this.env() };
      untracked(() => this.load(q));
    });
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  private load(q: { project: string; environment: string }): void {
    this.sub?.unsubscribe();
    this.loading.set(true);
    this.failure.set(null);
    this.sub = this.api.releases({ ...q, limit: PAGE }).subscribe({
      next: (page) => {
        this.releases.set(page.data);
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
    this.load({ project: this.project() ?? '', environment: this.env() });
  }

  protected more(): void {
    const cursor = this.nextCursor();
    if (!cursor || this.loadingMore()) {
      return;
    }
    this.loadingMore.set(true);
    const q = { project: this.project() ?? '', environment: this.env(), limit: PAGE, cursor };
    this.api.releases(q).subscribe({
      next: (page) => {
        this.releases.update((list) => [...list, ...page.data]);
        this.nextCursor.set(page.next_cursor);
        this.loadingMore.set(false);
      },
      error: (err) => {
        this.failure.set(err);
        this.loadingMore.set(false);
      },
    });
  }

  protected setProject(value: string): void {
    void this.router.navigate([], { queryParams: { project: value || null } });
  }
}

/** El despliegue más reciente de cada entorno; llegan ya del más nuevo al más viejo. */
function latestPerEnvironment(r: Release) {
  const seen = new Set<string>();
  return r.deploys.filter((d) => !seen.has(d.environment) && seen.add(d.environment));
}

/** El porcentaje de sesiones sin crash y la adopción, o «sin sesiones». */
function healthCell(r: Release): HealthCell {
  const h = r.health;
  const rate = percent(h.crash_free_sessions);
  const adoption = percent(h.adoption);
  return {
    none: h.crash_free_sessions === null,
    rate,
    tone: healthTone(h.crash_free_sessions),
    adoption: h.adoption === null ? '' : t('releases.health.adoption', { n: adoption }),
    title:
      h.crash_free_sessions === null
        ? t('releases.health.none')
        : t('releases.health.title', { rate, n: count(h.sessions), adoption }),
  };
}
