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
import type { Subscription } from 'rxjs';
import { Api, apiError, type CheckIn, type MonitorDetail } from '../../core/api';
import { ago, fullDate } from '../../core/format';
import { I18n, plural } from '../../core/i18n';
import { PageTitle } from '../../core/page-title';
import { CheckInStrip } from '../../ui/checkin-strip';
import { Icon } from '../../ui/icon';
import {
  CHECKIN_STATUS_LABEL,
  duration,
  MONITOR_STATUS_LABEL,
  STATUS_BADGE,
  scheduleText,
  until,
} from '../monitor-list/monitor-format';

interface HistoryRow {
  checkIn: CheckIn;
  started: string;
  startedAgo: string;
  expected: string;
  duration: string;
}

/** Un monitor: su horario, su estado y el historial de ejecuciones. */
@Component({
  selector: 'app-monitor-detail',
  imports: [RouterLink, Icon, CheckInStrip],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './monitor-detail.html',
  styleUrl: './monitor-detail.css',
})
export class MonitorDetailPage {
  /** El id del proyecto y el slug, de la ruta /monitors/:project/:slug. */
  readonly project = input.required<string>();
  readonly slug = input.required<string>();

  private readonly api = inject(Api);
  private readonly pageTitle = inject(PageTitle);
  protected readonly t = inject(I18n).t;
  protected readonly statusLabel = MONITOR_STATUS_LABEL;
  protected readonly checkInLabel = CHECKIN_STATUS_LABEL;
  protected readonly statusBadge = STATUS_BADGE;

  protected readonly detail = signal<MonitorDetail | null>(null);
  protected readonly history = signal<CheckIn[]>([]);
  protected readonly nextCursor = signal<string | undefined>(undefined);
  protected readonly loading = signal(true);
  protected readonly loadingMore = signal(false);
  protected readonly muting = signal(false);
  protected readonly notFound = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly facts = computed(() => {
    const d = this.detail();
    if (!d) {
      return null;
    }
    return {
      schedule: scheduleText(d.schedule),
      crontab: d.schedule.type === 'crontab',
      next: d.next_checkin_at ? until(d.next_checkin_at) : '—',
      nextTitle: d.next_checkin_at ? fullDate(d.next_checkin_at) : this.t('monitors.noNext'),
      last: d.last_checkin_at ? ago(d.last_checkin_at) : '—',
      lastTitle: d.last_checkin_at ? fullDate(d.last_checkin_at) : this.t('monitors.noCheckIns'),
      margin: plural('monitors.minutes', d.checkin_margin),
      runtime: plural('monitors.minutes', d.max_runtime),
    };
  });

  protected readonly rows = computed<HistoryRow[]>(() => {
    const now = Date.now();
    return this.history().map((c) => ({
      checkIn: c,
      started: fullDate(c.started_at),
      startedAgo: ago(c.started_at, now),
      expected: c.expected_at ? fullDate(c.expected_at) : '—',
      duration: duration(c.duration_ms),
    }));
  });

  protected readonly marks = computed(() => this.history().slice(0, 60));

  private sub?: Subscription;

  constructor() {
    effect(() => {
      const q = { project: Number(this.project()), slug: this.slug() };
      untracked(() => this.load(q.project, q.slug));
    });
    effect(() => {
      const d = this.detail();
      if (d) {
        this.pageTitle.set(() => this.t('title.monitorNamed', { name: d.name }));
      }
    });
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  private load(project: number, slug: string): void {
    this.sub?.unsubscribe();
    this.loading.set(true);
    this.notFound.set(false);
    this.failure.set(null);
    if (!Number.isInteger(project) || project <= 0 || !slug) {
      this.notFound.set(true);
      this.loading.set(false);
      return;
    }
    this.sub = this.api.monitor(project, slug).subscribe({
      next: (d) => {
        this.detail.set(d);
        this.history.set(d.checkins);
        this.nextCursor.set(d.next_cursor);
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
    });
  }

  protected retry(): void {
    this.load(Number(this.project()), this.slug());
  }

  protected more(): void {
    const d = this.detail();
    const cursor = this.nextCursor();
    if (!d || !cursor || this.loadingMore()) {
      return;
    }
    this.loadingMore.set(true);
    this.api.monitorCheckIns(d.project_id, d.slug, cursor).subscribe({
      next: (page) => {
        this.history.update((list) => [...list, ...page.data]);
        this.nextCursor.set(page.next_cursor);
        this.loadingMore.set(false);
      },
      error: (err) => {
        this.failure.set(err);
        this.loadingMore.set(false);
      },
    });
  }

  protected toggleMute(): void {
    const d = this.detail();
    if (!d || this.muting()) {
      return;
    }
    this.muting.set(true);
    this.api.setMonitorMuted(d.project_id, d.slug, !d.muted).subscribe({
      next: (m) => {
        this.detail.set({ ...d, ...m });
        this.muting.set(false);
      },
      error: (err) => {
        this.failure.set(err);
        this.muting.set(false);
      },
    });
  }
}
