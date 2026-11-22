import {
  ChangeDetectionStrategy,
  Component,
  computed,
  DestroyRef,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from '@angular/core';
import type { Subscription } from 'rxjs';
import {
  type Actor,
  Api,
  apiError,
  type ChangeSource,
  type HistoryEntry,
  type IssueComment,
  type IssueStatus,
} from '../../core/api';
import { ago, fullDate, STATUS_BADGE, STATUS_LABEL } from '../../core/format';
import { I18n, type Key } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import { Markdown } from './markdown';

const STATUS_ICON: Record<IssueStatus, string> = {
  open: 'arrow-counter-clockwise',
  resolved: 'check-circle',
  resolved_next: 'rocket-launch',
  silenced: 'bell-slash',
};

const SOURCE: Partial<Record<ChangeSource, Key>> = {
  ui: 'history.source.ui',
  api: 'history.source.api',
  mcp: 'history.source.mcp',
  linear: 'history.source.linear',
  github: 'history.source.github',
};

interface Row {
  entry: HistoryEntry;
  when: string;
  title: string;
  icon: string;
  /** El texto del cambio sin autor: «Reabierto al llegar un evento de 1.2.0». */
  system: string;
  source: string;
  comment: IssueComment | null;
  /** Una asignación a quien la hizo. */
  self: boolean;
}

/** El historial del error: comentarios y cambios de estado, con un cuadro para comentar. */
@Component({
  selector: 'app-history',
  imports: [Icon, Markdown],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './history.html',
  styleUrl: './history.css',
})
export class History {
  readonly issueId = input.required<number>();
  /** Cambia cuando el detalle cambia el estado, para volver a pedir el historial. */
  readonly version = input(0);
  readonly notice = output<string>();

  private readonly api = inject(Api);
  protected readonly t = inject(I18n).t;
  protected readonly statusLabel = STATUS_LABEL;
  protected readonly statusBadge = STATUS_BADGE;

  protected readonly entries = signal<HistoryEntry[]>([]);
  protected readonly loading = signal(true);
  protected readonly failed = signal(false);
  protected readonly draft = signal('');
  protected readonly sending = signal(false);
  protected readonly deleting = signal<number | null>(null);

  private sub?: Subscription;

  protected readonly rows = computed<Row[]>(() =>
    this.entries().map((entry) => {
      const comment: IssueComment | null =
        entry.type === 'comment'
          ? {
              id: entry.id,
              issue_id: this.issueId(),
              author: entry.author as Actor,
              body: entry.body ?? '',
              created_at: entry.at,
              mine: !!entry.mine,
            }
          : (entry.comment ?? null);
      let system = '';
      if (!entry.author && entry.source === 'deploy') {
        system = this.t('history.deployed', { release: entry.release ?? '—' });
      } else if (!entry.author && entry.source === 'ingest') {
        system = entry.release
          ? this.t('history.regressed', { release: entry.release })
          : this.t('history.regressedNoRelease');
      }
      const source = entry.source ? SOURCE[entry.source] : undefined;
      return {
        entry,
        when: ago(entry.at),
        title: fullDate(entry.at),
        icon: entry.to
          ? STATUS_ICON[entry.to]
          : entry.type === 'assignment' && !entry.assignee
            ? 'user-circle-dashed'
            : 'user',
        system,
        source: source ? this.t(source) : '',
        comment,
        self:
          !!entry.author &&
          !!entry.assignee &&
          entry.author.name === entry.assignee.name &&
          entry.author.kind === entry.assignee.kind,
      };
    }),
  );

  protected readonly canSend = computed(() => this.draft().trim() !== '' && !this.sending());

  constructor() {
    effect(() => {
      const id = this.issueId();
      this.version();
      untracked(() => this.load(id));
    });
    inject(DestroyRef).onDestroy(() => this.sub?.unsubscribe());
  }

  private load(id: number): void {
    this.sub?.unsubscribe();
    this.failed.set(false);
    this.sub = this.api.history(id).subscribe({
      next: (page) => {
        this.entries.set(page.data);
        this.loading.set(false);
      },
      error: () => {
        this.failed.set(true);
        this.loading.set(false);
      },
    });
  }

  protected retry(): void {
    this.loading.set(true);
    this.load(this.issueId());
  }

  protected initial(a: Actor | null): string {
    return (a?.name.trim()[0] ?? '?').toUpperCase();
  }

  protected onInput(event: Event): void {
    this.draft.set((event.target as HTMLTextAreaElement).value);
  }

  protected onKeydown(event: KeyboardEvent): void {
    if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
      event.preventDefault();
      this.send();
    }
  }

  protected send(): void {
    const body = this.draft().trim();
    if (!body || this.sending()) {
      return;
    }
    this.sending.set(true);
    this.api.addComment(this.issueId(), body).subscribe({
      next: (c) => {
        this.entries.update((list) => [
          ...list,
          {
            type: 'comment',
            id: c.id,
            at: c.created_at,
            author: c.author,
            body: c.body,
            mine: true,
          },
        ]);
        this.draft.set('');
        this.sending.set(false);
        this.notice.emit(this.t('toast.commented'));
      },
      error: (err) => {
        this.sending.set(false);
        this.notice.emit(apiError(err));
      },
    });
  }

  protected remove(comment: IssueComment): void {
    if (this.deleting() !== null) {
      return;
    }
    this.deleting.set(comment.id);
    this.api.deleteComment(this.issueId(), comment.id).subscribe({
      next: () => {
        this.deleting.set(null);
        // Un comentario suelto desaparece; el de un cambio deja el cambio.
        this.entries.update((list) =>
          list
            .filter((e) => !(e.type === 'comment' && e.id === comment.id))
            .map((e) => (e.comment?.id === comment.id ? { ...e, comment: undefined } : e)),
        );
        this.notice.emit(this.t('toast.commentDeleted'));
      },
      error: (err) => {
        this.deleting.set(null);
        this.notice.emit(apiError(err));
      },
    });
  }
}
