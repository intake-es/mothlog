import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  signal,
  untracked,
} from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { Api, apiError, type Project } from '../../core/api';
import { I18n, type Key } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import { Pick, type PickOption } from '../../ui/pick';
import { AlertRules } from './alert-rules';
import { ProjectFilters } from './project-filters';

type Section = 'alerts' | 'filters';

const SECTIONS: { value: Section; label: Key; icon: string }[] = [
  { value: 'alerts', label: 'settings.alerts', icon: 'bell' },
  { value: 'filters', label: 'settings.filters', icon: 'funnel' },
];

/** Los ajustes de un proyecto: /projects/:id/settings/:section; /settings va al primero. */
@Component({
  selector: 'app-project-settings',
  imports: [RouterLink, Icon, Pick, AlertRules, ProjectFilters],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './project-settings.html',
  styleUrl: './project-settings.css',
})
export class ProjectSettingsPage {
  readonly id = input<string>();
  readonly section = input<string>();

  private readonly api = inject(Api);
  private readonly router = inject(Router);
  protected readonly t = inject(I18n).t;
  protected readonly sections = SECTIONS;

  protected readonly projects = signal<Project[] | null>(null);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );

  protected readonly projectId = computed(() => Number(this.id()) || 0);
  protected readonly current = computed<Section>(() =>
    this.section() === 'filters' ? 'filters' : 'alerts',
  );
  protected readonly project = computed(
    () => this.projects()?.find((p) => p.id === this.projectId()) ?? null,
  );
  protected readonly projectOptions = computed<PickOption[]>(() =>
    (this.projects() ?? []).map((p) => ({ value: String(p.id), label: p.name })),
  );

  constructor() {
    this.load();
    // Sin proyecto en la ruta, o con uno que no existe, se va al primero.
    effect(() => {
      const ps = this.projects();
      const id = this.projectId();
      if (ps?.length && !ps.some((p) => p.id === id)) {
        untracked(() => this.go(ps[0].id, this.current(), true));
      }
    });
  }

  protected load(): void {
    this.failure.set(null);
    this.projects.set(null);
    this.api.projects().subscribe({
      next: (page) => this.projects.set(page.data),
      error: (err) => {
        this.failure.set(err);
        this.projects.set([]);
      },
    });
  }

  protected chooseProject(id: string): void {
    this.go(Number(id), this.current(), false);
  }

  private go(id: number, section: Section, replaceUrl: boolean): void {
    void this.router.navigate(['/projects', id, 'settings', section], { replaceUrl });
  }
}
