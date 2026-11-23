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
import { forkJoin, type Subscription } from 'rxjs';
import { Api, apiError, type Monitor, type Project, type UptimeMonitor } from '../../core/api';
import { copyText } from '../../core/clipboard';
import { ago, count, fullDate } from '../../core/format';
import { I18n, plural } from '../../core/i18n';
import { CheckInStrip } from '../../ui/checkin-strip';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';
import { UptimeStrip } from '../../ui/uptime-strip';
import { MONITOR_STATUS_LABEL, STATUS_BADGE, scheduleText, until } from './monitor-format';
import {
  intervalText,
  percent,
  percentClass,
  shortURL,
  UPTIME_STATE_BADGE,
  UPTIME_STATE_LABEL,
  type UptimeState,
  uptimeState,
} from './uptime-format';

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

interface UptimeRow {
  monitor: UptimeMonitor;
  link: (string | number)[];
  state: UptimeState;
  url: string;
  every: string;
  day: string;
  dayClass: string;
  month: string;
  monthClass: string;
}

/**
 * Los monitores de disponibilidad con su estado, su porcentaje y sus últimas
 * comprobaciones, y los crons con sus últimas ejecuciones; los que fallan, primero.
 */
@Component({
  selector: 'app-monitor-list',
  imports: [RouterLink, Icon, Pick, CheckInStrip, UptimeStrip],
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
  protected readonly uptimeLabel = UPTIME_STATE_LABEL;
  protected readonly uptimeBadge = UPTIME_STATE_BADGE;

  protected readonly projects = signal<Project[]>([]);
  protected readonly monitors = signal<Monitor[]>([]);
  protected readonly uptime = signal<UptimeMonitor[]>([]);
  protected readonly loading = signal(true);
  protected readonly example = signal(false);
  protected readonly copied = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly projectOptions = computed<PickOption[]>(() => [
    { value: '', label: this.t('list.allProjects') },
    ...this.projects().map((p) => ({ value: p.name, label: p.name })),
  ]);

  protected readonly title = computed(() =>
    this.loading() || this.error() ? null : count(this.monitors().length + this.uptime().length),
  );

  protected readonly failing = computed(
    () => this.monitors().filter((m) => ['error', 'missed', 'timeout'].includes(m.status)).length,
  );

  protected readonly down = computed(
    () => this.uptime().filter((m) => uptimeState(m) === 'down').length,
  );

  protected readonly summary = computed(() =>
    [
      this.down() ? plural('monitors.down', this.down()) : '',
      this.failing() ? plural('monitors.failing', this.failing()) : '',
    ]
      .filter(Boolean)
      .join(' '),
  );

  protected readonly uptimeRows = computed<UptimeRow[]>(() =>
    this.uptime().map((m) => ({
      monitor: m,
      link: ['/monitors', m.project_id, 'uptime', m.slug],
      state: uptimeState(m),
      url: shortURL(m.url),
      every: intervalText(m.interval_seconds),
      day: percent(m.uptime_24h),
      dayClass: percentClass(m.uptime_24h),
      month: percent(m.uptime_30d),
      monthClass: percentClass(m.uptime_30d),
    })),
  );

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
    this.sub = forkJoin([this.api.uptimeMonitors(project), this.api.monitors(project)]).subscribe({
      next: ([uptime, crons]) => {
        this.uptime.set(uptime.data);
        this.monitors.set(crons.data);
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

  protected async copyExample(): Promise<void> {
    try {
      await copyText(Promise.resolve(this.t('monitors.empty.example')));
      this.copied.set(true);
      setTimeout(() => this.copied.set(false), 2000);
    } catch {
      this.copied.set(false);
    }
  }

  protected setProject(value: string): void {
    void this.router.navigate([], { queryParams: { project: value || null } });
  }
}
