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
  type Project,
  type UptimeInput,
  type UptimeMethod,
  type UptimeMonitor,
} from '../../core/api';
import { I18n } from '../../core/i18n';
import { PageTitle } from '../../core/page-title';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';
import { INTERVALS, intervalText } from '../monitor-list/uptime-format';

export interface UptimeDraft {
  projectId: number;
  name: string;
  url: string;
  method: UptimeMethod;
  interval: number;
  timeout: number;
  expected: string;
  threshold: number;
  paused: boolean;
}

export function newUptimeDraft(projectId = 0): UptimeDraft {
  return {
    projectId,
    name: '',
    url: '',
    method: 'GET',
    interval: 60,
    timeout: 10,
    expected: '200-299',
    threshold: 2,
    paused: false,
  };
}

export function uptimeDraftOf(m: UptimeMonitor): UptimeDraft {
  return {
    projectId: m.project_id,
    name: m.name,
    url: m.url,
    method: m.method,
    interval: m.interval_seconds,
    timeout: m.timeout_seconds,
    expected: m.expected_status,
    threshold: m.failure_threshold,
    paused: m.paused,
  };
}

export function uptimeInputOf(d: UptimeDraft): UptimeInput {
  return {
    name: d.name.trim(),
    url: d.url.trim(),
    method: d.method,
    interval_seconds: d.interval,
    timeout_seconds: d.timeout,
    expected_status: d.expected.trim(),
    failure_threshold: d.threshold,
    paused: d.paused,
  };
}

/** Crear o editar un monitor de disponibilidad. */
@Component({
  selector: 'app-uptime-form',
  imports: [RouterLink, Icon, Pick],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './uptime-form.html',
  styleUrls: ['../project-settings/settings.css', './uptime-form.css'],
})
export class UptimeFormPage {
  /**
   * Al editar, el id del proyecto de la ruta /monitors/:project/uptime/:slug/edit;
   * al crear, el nombre del proyecto de ?project= para elegirlo de entrada.
   */
  readonly project = input<string>();
  readonly slug = input<string>();

  private readonly api = inject(Api);
  private readonly router = inject(Router);
  private readonly pageTitle = inject(PageTitle);
  protected readonly t = inject(I18n).t;
  protected readonly methods: UptimeMethod[] = ['GET', 'HEAD'];

  protected readonly projects = signal<Project[]>([]);
  protected readonly editing = signal<UptimeMonitor | null>(null);
  protected readonly draft = signal<UptimeDraft>(newUptimeDraft());
  protected readonly loading = signal(true);
  protected readonly notFound = signal(false);
  protected readonly busy = signal(false);
  protected readonly confirmDelete = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly projectOptions = computed<PickOption[]>(() =>
    this.projects().map((p) => ({ value: String(p.id), label: p.name })),
  );

  protected readonly intervalOptions = computed<PickOption[]>(() =>
    INTERVALS.map((s) => ({ value: String(s), label: intervalText(s) })),
  );

  protected readonly backLink = computed(() => {
    const m = this.editing();
    return m ? ['/monitors', m.project_id, 'uptime', m.slug] : ['/monitors'];
  });

  protected readonly heading = computed(() => {
    const m = this.editing();
    return m ? this.t('uptimeForm.editTitle', { name: m.name }) : this.t('uptimeForm.newTitle');
  });

  protected readonly valid = computed(() => {
    const d = this.draft();
    return (
      d.projectId > 0 && d.name.trim() !== '' && d.url.trim() !== '' && d.expected.trim() !== ''
    );
  });

  private sub?: Subscription;

  constructor() {
    effect(() => {
      const q = { project: this.project() ?? '', slug: this.slug() ?? '' };
      untracked(() => this.load(q.project, q.slug));
    });
    effect(() => {
      const m = this.editing();
      if (m) {
        this.pageTitle.set(() => this.t('title.uptimeEditNamed', { name: m.name }));
      }
    });
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  private load(project: string, slug: string): void {
    this.sub?.unsubscribe();
    this.loading.set(true);
    this.notFound.set(false);
    this.failure.set(null);
    if (slug) {
      const id = Number(project);
      if (!Number.isInteger(id) || id <= 0) {
        this.notFound.set(true);
        this.loading.set(false);
        return;
      }
      this.sub = this.api.uptimeMonitor(id, slug).subscribe({
        next: (m) => {
          this.editing.set(m);
          this.draft.set(uptimeDraftOf(m));
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
      return;
    }
    this.editing.set(null);
    this.sub = this.api.projects().subscribe({
      next: (page) => {
        this.projects.set(page.data);
        const pick = page.data.find((p) => p.name === project) ?? page.data[0];
        this.draft.set(newUptimeDraft(pick?.id ?? 0));
        this.loading.set(false);
      },
      error: (err) => {
        this.failure.set(err);
        this.loading.set(false);
      },
    });
  }

  protected patch(p: Partial<UptimeDraft>): void {
    this.draft.update((d) => ({ ...d, ...p }));
    this.confirmDelete.set(false);
  }

  protected num(v: string): number {
    const n = Number.parseInt(v, 10);
    return Number.isNaN(n) ? 0 : n;
  }

  protected save(): void {
    if (this.busy() || !this.valid()) {
      return;
    }
    const d = this.draft();
    const m = this.editing();
    this.busy.set(true);
    this.failure.set(null);
    const req = m
      ? this.api.updateUptimeMonitor(m.project_id, m.slug, uptimeInputOf(d))
      : this.api.createUptimeMonitor(d.projectId, uptimeInputOf(d));
    req.subscribe({
      next: (saved) => {
        this.busy.set(false);
        void this.router.navigate(['/monitors', saved.project_id, 'uptime', saved.slug]);
      },
      error: (err) => {
        this.failure.set(err);
        this.busy.set(false);
      },
    });
  }

  protected remove(): void {
    const m = this.editing();
    if (!m || this.busy()) {
      return;
    }
    if (!this.confirmDelete()) {
      this.confirmDelete.set(true);
      return;
    }
    this.busy.set(true);
    this.api.deleteUptimeMonitor(m.project_id, m.slug).subscribe({
      next: () => {
        this.busy.set(false);
        void this.router.navigate(['/monitors']);
      },
      error: (err) => {
        this.failure.set(err);
        this.busy.set(false);
      },
    });
  }
}
