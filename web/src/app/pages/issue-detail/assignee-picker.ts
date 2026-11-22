import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  type ElementRef,
  inject,
  input,
  output,
  signal,
  viewChild,
} from '@angular/core';
import type { Subscription } from 'rxjs';
import { Api, type Assignee, apiError, type Issue } from '../../core/api';
import { I18n } from '../../core/i18n';
import { Session } from '../../core/session';
import { Avatar } from '../../ui/avatar';
import { Icon } from '../../ui/icon';

type Target = Pick<Assignee, 'name' | 'kind'>;

/** Quién lleva el error y el menú para cambiarlo: las personas y los tokens activos. */
@Component({
  selector: 'app-assignee-picker',
  imports: [Avatar, Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(keydown.escape)': 'close()' },
  templateUrl: './assignee-picker.html',
  styleUrl: './assignee-picker.css',
})
export class AssigneePicker {
  readonly issueId = input.required<number>();
  readonly assignee = input<Assignee | null | undefined>(null);
  readonly changed = output<Issue>();
  readonly notice = output<string>();

  private readonly api = inject(Api);
  private readonly session = inject(Session);
  protected readonly t = inject(I18n).t;

  protected readonly open = signal(false);
  protected readonly busy = signal(false);
  protected readonly options = signal<Assignee[] | null>(null);
  protected readonly failed = signal(false);
  protected readonly filter = signal('');
  private readonly filterInput = viewChild<ElementRef<HTMLInputElement>>('filterInput');
  private sub?: Subscription;

  protected readonly me = computed(() => this.session.info()?.user?.login ?? '');

  protected readonly label = computed(() => {
    const a = this.assignee();
    if (!a) {
      return this.t('assignee.none');
    }
    const name = a.display_name || a.name;
    return a.active ? name : this.t('assignee.revoked', { name });
  });

  private readonly matches = computed(() => {
    const q = this.filter().trim().toLowerCase();
    return (this.options() ?? []).filter(
      (o) =>
        !q || o.name.toLowerCase().includes(q) || (o.display_name ?? '').toLowerCase().includes(q),
    );
  });
  protected readonly people = computed(() => this.matches().filter((o) => o.kind === 'user'));
  protected readonly agents = computed(() => this.matches().filter((o) => o.kind === 'token'));
  protected readonly mine = computed(() => {
    const a = this.assignee();
    return !!a && a.kind === 'user' && a.name === this.me();
  });

  constructor() {
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  protected toggle(): void {
    if (this.open()) {
      this.close();
      return;
    }
    this.open.set(true);
    this.filter.set('');
    this.failed.set(false);
    this.sub?.unsubscribe();
    this.sub = this.api.assignees().subscribe({
      next: (page) => this.options.set(page.data),
      error: () => this.failed.set(true),
    });
    setTimeout(() => this.filterInput()?.nativeElement.focus());
  }

  protected close(): void {
    this.open.set(false);
  }

  protected current(o: Target): boolean {
    const a = this.assignee();
    return !!a && a.name === o.name && a.kind === o.kind;
  }

  protected onFilter(event: Event): void {
    this.filter.set((event.target as HTMLInputElement).value);
  }

  /** Intro en el filtro elige el primero que queda. */
  protected pickFirst(event: Event): void {
    event.preventDefault();
    const first = this.people()[0] ?? this.agents()[0];
    if (first) {
      this.choose(first);
    }
  }

  protected choose(to: Target | null): void {
    if (this.busy() || (to && this.current(to)) || (!to && !this.assignee())) {
      this.close();
      return;
    }
    this.close();
    this.busy.set(true);
    this.api.assign(this.issueId(), to).subscribe({
      next: (issue) => {
        this.busy.set(false);
        this.changed.emit(issue);
        const a = issue.assignee;
        this.notice.emit(
          a
            ? this.t('toast.assigned', { name: a.display_name || a.name })
            : this.t('toast.unassigned'),
        );
      },
      error: (err) => {
        this.busy.set(false);
        this.notice.emit(apiError(err));
      },
    });
  }

  protected assignMe(): void {
    this.choose({ name: this.me(), kind: 'user' });
  }
}
