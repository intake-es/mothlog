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
import { Api, apiError, type UptimeDetail, type UptimeIncident } from '../../core/api';
import { ago, fullDate } from '../../core/format';
import { I18n } from '../../core/i18n';
import { PageTitle } from '../../core/page-title';
import { Icon } from '../../ui/icon';
import { ResponseChart } from '../../ui/response-chart';
import { UptimeStrip } from '../../ui/uptime-strip';
import { duration } from '../monitor-list/monitor-format';
import {
  intervalText,
  percent,
  percentClass,
  UPTIME_STATE_BADGE,
  UPTIME_STATE_LABEL,
  uptimeState,
} from '../monitor-list/uptime-format';

interface IncidentRow {
  incident: UptimeIncident;
  started: string;
  startedAgo: string;
  ended: string;
  duration: string;
  ongoing: boolean;
}

/** Un monitor de disponibilidad: su estado, su porcentaje, sus tiempos de respuesta y sus incidentes. */
@Component({
  selector: 'app-uptime-detail',
  imports: [RouterLink, Icon, UptimeStrip, ResponseChart],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './uptime-detail.html',
  styleUrls: ['../monitor-detail/monitor-detail.css', './uptime-detail.css'],
})
export class UptimeDetailPage {
  /** El id del proyecto y el slug, de la ruta /monitors/:project/uptime/:slug. */
  readonly project = input.required<string>();
  readonly slug = input.required<string>();

  private readonly api = inject(Api);
  private readonly pageTitle = inject(PageTitle);
  protected readonly t = inject(I18n).t;
  protected readonly stateLabel = UPTIME_STATE_LABEL;
  protected readonly stateBadge = UPTIME_STATE_BADGE;

  protected readonly detail = signal<UptimeDetail | null>(null);
  protected readonly loading = signal(true);
  protected readonly pausing = signal(false);
  protected readonly notFound = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly state = computed(() => {
    const d = this.detail();
    return d ? uptimeState(d) : 'pending';
  });

  protected readonly facts = computed(() => {
    const d = this.detail();
    if (!d) {
      return null;
    }
    return {
      day: percent(d.uptime_24h),
      dayClass: percentClass(d.uptime_24h),
      month: percent(d.uptime_30d),
      monthClass: percentClass(d.uptime_30d),
      response:
        d.last_checked_at === null
          ? '—'
          : [d.last_status_code ?? '', duration(d.last_duration_ms)].filter(Boolean).join(' · '),
      last: d.last_checked_at ? ago(d.last_checked_at) : NONE,
      lastTitle: d.last_checked_at ? fullDate(d.last_checked_at) : '',
      every: intervalText(d.interval_seconds),
      config: this.t('uptime.config', {
        timeout: d.timeout_seconds,
        codes: d.expected_status,
        n: d.failure_threshold,
      }),
      downSince: d.down_since ? this.t('uptime.downSince', { when: ago(d.down_since) }) : '',
      downTitle: d.down_since ? fullDate(d.down_since) : '',
    };
  });

  protected readonly incidents = computed<IncidentRow[]>(() => {
    const now = Date.now();
    return (this.detail()?.incidents ?? []).map((i) => {
      const ongoing = i.ended_at === null;
      return {
        incident: i,
        started: fullDate(i.started_at),
        startedAgo: ago(i.started_at, now),
        ended: i.ended_at ? fullDate(i.ended_at) : this.t('uptime.ongoing'),
        duration: duration(ongoing ? now - Date.parse(i.started_at) : i.duration_ms),
        ongoing,
      };
    });
  });

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
    this.sub = this.api.uptimeMonitor(project, slug).subscribe({
      next: (d) => {
        this.detail.set(d);
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

  protected togglePause(): void {
    const d = this.detail();
    if (!d || this.pausing()) {
      return;
    }
    this.pausing.set(true);
    this.api.updateUptimeMonitor(d.project_id, d.slug, { paused: !d.paused }).subscribe({
      next: (m) => {
        this.detail.set({ ...d, ...m });
        this.pausing.set(false);
      },
      error: (err) => {
        this.failure.set(err);
        this.pausing.set(false);
      },
    });
  }
}

const NONE = '—';
