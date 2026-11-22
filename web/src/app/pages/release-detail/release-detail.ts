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
import { Api, apiError, type ReleaseDetail, type ReleaseIssue } from '../../core/api';
import { environment } from '../../core/environment';
import {
  ago,
  count,
  fullDate,
  healthTone,
  percent,
  STATUS_BADGE,
  STATUS_LABEL,
  splitTitle,
} from '../../core/format';
import { I18n, type Key, plural } from '../../core/i18n';
import { PageTitle } from '../../core/page-title';
import { Icon } from '../../ui/icon';
import { SessionChart } from './session-chart';

type Group = 'new' | 'regressed' | 'continued' | 'stopped';

interface Row {
  issue: ReleaseIssue;
  type: string;
  message: string;
  events: string;
  seen: string;
  seenTitle: string;
  fixedHere: boolean;
}

interface Section {
  key: Group;
  title: Key;
  hint: string;
  rows: Row[];
}

const TITLES: Record<Group, Key> = {
  new: 'release.new',
  regressed: 'release.regressed',
  continued: 'release.continued',
  stopped: 'release.stopped',
};

/** Una release: sus despliegues, los errores que trajo y los que dejaron de salir con ella. */
@Component({
  selector: 'app-release-detail',
  imports: [RouterLink, Icon, SessionChart],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './release-detail.html',
  styleUrl: './release-detail.css',
})
export class ReleaseDetailPage {
  /** El id del proyecto y la versión, de la ruta /releases/:project/:version. */
  readonly project = input.required<string>();
  readonly version = input.required<string>();

  private readonly api = inject(Api);
  private readonly pageTitle = inject(PageTitle);
  protected readonly t = inject(I18n).t;
  protected readonly statusLabel = STATUS_LABEL;
  protected readonly statusBadge = STATUS_BADGE;

  protected readonly detail = signal<ReleaseDetail | null>(null);
  protected readonly loading = signal(true);
  protected readonly notFound = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly firstSeen = computed(() => {
    const d = this.detail();
    return d ? this.t('release.firstSeen', { ago: ago(d.first_seen) }) : '';
  });

  protected readonly deploys = computed(() =>
    (this.detail()?.deploys ?? []).map((d) => ({
      ...d,
      when: fullDate(d.deployed_at),
      ago: ago(d.deployed_at),
    })),
  );

  /** Los porcentajes sin crash, la adopción, la serie y, sin entorno elegido, cada entorno. */
  protected readonly health = computed(() => {
    const d = this.detail();
    if (!d) {
      return null;
    }
    const h = d.health;
    return {
      has: h.sessions > 0,
      sessions: percent(h.crash_free_sessions),
      sessionsTone: healthTone(h.crash_free_sessions),
      sessionsSub: this.t('release.health.sessionsCrashed', {
        crashed: count(h.sessions_crashed),
        n: count(h.sessions),
      }),
      users: percent(h.crash_free_users),
      usersTone: healthTone(h.crash_free_users),
      usersSub: h.users
        ? this.t('release.health.usersCrashed', {
            crashed: count(h.users_crashed),
            n: count(h.users),
          })
        : this.t('release.health.noUsers'),
      adoption: percent(h.adoption),
      adoptionSub:
        h.adoption === null
          ? this.t('release.health.noRecent')
          : plural('release.health.recent', h.sessions_24h),
      series: d.health_series,
      envs:
        h.environment || d.health_by_environment.length < 2
          ? []
          : d.health_by_environment.map((e) => ({
              name: e.environment || this.t('release.health.noEnvironment'),
              sessions: count(e.sessions),
              crashed: count(e.sessions_crashed),
              rate: percent(e.crash_free_sessions),
              tone: healthTone(e.crash_free_sessions),
              users: percent(e.crash_free_users),
            })),
    };
  });

  protected readonly sections = computed<Section[]>(() => {
    const d = this.detail();
    if (!d) {
      return [];
    }
    const now = Date.now();
    const row =
      (inPrevious: boolean) =>
      (issue: ReleaseIssue): Row => {
        const { type, message } = splitTitle(issue.title, issue.type);
        return {
          issue,
          type,
          message,
          events: inPrevious
            ? plural('release.inPrevious', issue.release_events, { release: d.previous ?? '' })
            : plural('release.inRelease', issue.release_events),
          seen: ago(issue.release_last_seen, now),
          seenTitle: fullDate(issue.release_last_seen),
          fixedHere: issue.fixed_in_release === d.version,
        };
      };
    const hints: Record<Group, string> = {
      new: this.t('release.new.hint'),
      regressed: this.t('release.regressed.hint'),
      continued: this.t('release.continued.hint'),
      stopped: d.previous
        ? this.t('release.stopped.hint', { previous: d.previous })
        : this.t('release.stopped.hintFirst'),
    };
    return (['new', 'regressed', 'continued', 'stopped'] as Group[]).map((key) => ({
      key,
      title: TITLES[key],
      hint: hints[key],
      rows: d[key].map(row(key === 'stopped')),
    }));
  });

  protected readonly count = count;

  private sub?: Subscription;

  constructor() {
    effect(() => {
      const q = { project: Number(this.project()), version: this.version(), env: environment() };
      untracked(() => this.load(q.project, q.version, q.env));
    });
    effect(() => {
      const d = this.detail();
      if (d) {
        this.pageTitle.set(() => this.t('title.releaseNamed', { version: d.version }));
      }
    });
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  private load(project: number, version: string, env: string): void {
    this.sub?.unsubscribe();
    this.loading.set(true);
    this.notFound.set(false);
    this.failure.set(null);
    if (!Number.isInteger(project) || project <= 0 || !version) {
      this.notFound.set(true);
      this.loading.set(false);
      return;
    }
    this.sub = this.api.release(project, version, env).subscribe({
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
    this.load(Number(this.project()), this.version(), environment());
  }
}
