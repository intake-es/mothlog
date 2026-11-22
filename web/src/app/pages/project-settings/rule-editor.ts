import {
  ChangeDetectionStrategy,
  Component,
  computed,
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from '@angular/core';
import { type ActionType, Api, apiError, type Rule } from '../../core/api';
import { copyText } from '../../core/clipboard';
import { I18n, type Key } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import {
  ACTION_FIELDS,
  ACTION_HINT,
  ACTION_ICON,
  ACTION_TYPES,
  type ActionDraft,
  type ActionField,
  type Draft,
  draftOf,
  emptyAction,
  inputOf,
  LEVELS,
  newDraft,
  textParts,
} from './rule-draft';

type TestState = { state: 'sending' | 'ok' | 'error'; message?: string };

/** El editor de una regla: condiciones, filtros, destinos con su prueba y el límite. */
@Component({
  selector: 'app-rule-editor',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './rule-editor.html',
  styleUrls: ['./settings.css', './rule-editor.css'],
})
export class RuleEditor {
  readonly projectId = input.required<number>();
  readonly rule = input.required<Rule | null>();
  /** El secreto de webhook recién generado, que solo llega entero al guardar. */
  readonly secret = input('');
  readonly saved = output<Rule>();
  readonly dismissed = output<void>();
  readonly deleted = output<number>();
  readonly cancelled = output<void>();

  private readonly api = inject(Api);
  protected readonly i18n = inject(I18n);
  protected readonly t = this.i18n.t;
  protected readonly types = ACTION_TYPES;
  protected readonly icons = ACTION_ICON;
  protected readonly fields = ACTION_FIELDS;
  protected readonly hints = ACTION_HINT;
  protected readonly levels = LEVELS;

  protected readonly draft = signal<Draft>(newDraft());
  private readonly initial = signal('');
  protected readonly dirty = computed(() => JSON.stringify(this.draft()) !== this.initial());
  protected readonly busy = signal(false);
  protected readonly confirmDelete = signal(false);
  protected readonly addOpen = signal(false);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );
  protected readonly tests = signal<Record<string, TestState>>({});
  protected readonly secretCopied = signal(false);

  protected readonly frequencyParts = computed(() => {
    this.i18n.lang();
    return textParts(this.t('editor.frequency'));
  });
  protected readonly usersParts = computed(() => {
    this.i18n.lang();
    return textParts(this.t('editor.users'));
  });
  protected readonly throttleParts = computed(() => {
    this.i18n.lang();
    return textParts(this.t('editor.throttle'));
  });
  /** Los ids de las acciones que ya están guardadas: solo esas se pueden probar. */
  private readonly savedActions = computed(
    () => new Set((this.rule()?.actions ?? []).map((a) => a.id)),
  );

  constructor() {
    effect(() => {
      const r = this.rule();
      untracked(() => {
        const d = r ? draftOf(r) : newDraft();
        this.draft.set(d);
        this.initial.set(JSON.stringify(d));
        this.tests.set({});
        this.confirmDelete.set(false);
      });
    });
  }

  protected patch(change: Partial<Draft>): void {
    this.draft.update((d) => ({ ...d, ...change }));
  }

  protected num(value: string): number {
    const n = Number.parseInt(value, 10);
    return Number.isFinite(n) ? n : 0;
  }

  protected toggleLevel(level: string): void {
    const levels = this.draft().levels;
    this.patch({
      levels: levels.includes(level) ? levels.filter((l) => l !== level) : [...levels, level],
    });
  }

  protected setTag(i: number, change: Partial<{ key: string; value: string }>): void {
    this.patch({
      tags: this.draft().tags.map((tag, j) => (j === i ? { ...tag, ...change } : tag)),
    });
  }

  protected addTag(): void {
    this.patch({ tags: [...this.draft().tags, { key: '', value: '' }] });
  }

  protected removeTag(i: number): void {
    this.patch({ tags: this.draft().tags.filter((_, j) => j !== i) });
  }

  protected setAction(i: number, field: ActionField, value: string): void {
    this.patch({
      actions: this.draft().actions.map((a, j) => (j === i ? { ...a, [field]: value } : a)),
    });
  }

  protected addAction(type: ActionType): void {
    this.addOpen.set(false);
    this.patch({ actions: [...this.draft().actions, emptyAction(type)] });
  }

  protected removeAction(i: number): void {
    this.patch({ actions: this.draft().actions.filter((_, j) => j !== i) });
  }

  protected actionLabel(type: ActionType): string {
    return this.t(`action.${type}` as Key);
  }

  protected isMasked(a: ActionDraft, field: ActionField): boolean {
    return a[field].includes('…');
  }

  protected canTest(a: ActionDraft): boolean {
    return !!this.rule() && !this.dirty() && this.savedActions().has(a.id);
  }

  protected save(): void {
    if (this.busy()) {
      return;
    }
    this.busy.set(true);
    this.failure.set(null);
    const rule = this.rule();
    const body = inputOf(this.draft());
    const req = rule
      ? this.api.updateRule(this.projectId(), rule.id, body)
      : this.api.createRule(this.projectId(), body);
    req.subscribe({
      next: (saved) => {
        this.busy.set(false);
        this.secretCopied.set(false);
        this.saved.emit(saved);
      },
      error: (err) => {
        this.busy.set(false);
        this.failure.set(err);
      },
    });
  }

  protected remove(): void {
    const rule = this.rule();
    if (!rule) {
      return;
    }
    if (!this.confirmDelete()) {
      this.confirmDelete.set(true);
      return;
    }
    this.busy.set(true);
    this.api.deleteRule(this.projectId(), rule.id).subscribe({
      next: () => {
        this.busy.set(false);
        this.deleted.emit(rule.id);
      },
      error: (err) => {
        this.busy.set(false);
        this.failure.set(err);
      },
    });
  }

  protected test(a: ActionDraft): void {
    const rule = this.rule();
    if (!rule || !this.canTest(a)) {
      return;
    }
    this.tests.update((s) => ({ ...s, [a.id]: { state: 'sending' } }));
    this.api.testRule(this.projectId(), rule.id, a.id).subscribe({
      next: () => this.tests.update((s) => ({ ...s, [a.id]: { state: 'ok' } })),
      error: (err) =>
        this.tests.update((s) => ({ ...s, [a.id]: { state: 'error', message: apiError(err) } })),
    });
  }

  protected copySecret(secret: string): void {
    void copyText(Promise.resolve(secret)).then(() => this.secretCopied.set(true));
  }
}
