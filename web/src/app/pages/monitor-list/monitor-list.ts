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
import { Api, apiError, type Monitor, type Project } from '../../core/api';
import { ago, count, fullDate } from '../../core/format';
import { I18n, plural } from '../../core/i18n';
import { CheckInStrip } from '../../ui/checkin-strip';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';
import { MONITOR_STATUS_LABEL, STATUS_BADGE, scheduleText, until } from './monitor-format';

interface Row {
  monitor: Monitor;
  link: (string | number)[];
  schedule: string;
  crontab: boolean;
  next: string;
  nextTitle: string;
  last: string;
  lastTitle: string;
}

/** Los crons con su estado y sus últimas ejecuciones; los que fallan, primero. */
@Component({
  selector: 'app-monitor-list',
  imports: [RouterLink, Icon, Pick, CheckInStrip],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './monitor-list.html',
  styleUrl: './monitor-list.css',
})
export class MonitorListPage {
  /** ?project= en la URL, como en las otras listas. */
  readonly project = input<string>();

  private readonly api = inject(Api);
  private readonly router = inject(Router);
  protected readonly t = inject(I18n).t;
  protected readonly statusLabel = MONITOR_STATUS_LABEL;
  protected readonly statusBadge = STATUS_BADGE;

  protected readonly projects = signal<Project[]>([]);
  protected readonly monitors = signal<Monitor[]>([]);
  protected readonly loading = signal(true);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly projectOptions = computed<PickOption[]>(() => [
    { value: '', label: this.t('list.allProjects') },
    ...this.projects().map((p) => ({ value: p.name, label: p.name })),
  ]);

  protected readonly title = computed(() =>
    this.loading() || this.error() ? null : count(this.monitors().length),
  );

  protected readonly failing = computed(
    () => this.monitors().filter((m) => ['error', 'missed', 'timeout'].includes(m.status)).length,
  );

  protected readonly failingText = computed(() => plural('monitors.failing', this.failing()));

  protected readonly rows = computed<Row[]>(() => {
    const now = Date.now();
    return this.monitors().map((m) => ({
      monitor: m,
      link: ['/monitors', m.project_id, m.slug],
      schedule: scheduleText(m.schedule),
      crontab: m.schedule.type === 'crontab',
      next: m.muted ? this.t('monitors.mutedShort') : until(m.next_checkin_at, now),
      nextTitle: m.next_checkin_at ? fullDate(m.next_checkin_at) : this.t('monitors.noNext'),
      last: ago(m.last_checkin_at, now),
      lastTitle: m.last_checkin_at ? fullDate(m.last_checkin_at) : this.t('monitors.noCheckIns'),
    }));
  });

  private sub?: Subscription;

  constructor() {
    this.api.projects().subscribe({
      next: (page) => this.projects.set(page.data),
      error: () => this.projects.set([]),
    });
    effect(() => {
      const project = this.project() ?? '';
      untracked(() => this.load(project));
    });
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  private load(project: string): void {
    this.sub?.unsubscribe();
    this.loading.set(true);
    this.failure.set(null);
    this.sub = this.api.monitors(project).subscribe({
      next: (page) => {
        this.monitors.set(page.data);
        this.loading.set(false);
      },
      error: (err) => {
        this.failure.set(err);
        this.loading.set(false);
      },
    });
  }

  protected retry(): void {
    this.load(this.project() ?? '');
  }

  protected setProject(value: string): void {
    void this.router.navigate([], { queryParams: { project: value || null } });
  }
}
