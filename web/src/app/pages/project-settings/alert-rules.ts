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
import { Api, apiError, type Rule } from '../../core/api';
import { I18n } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import { ACTION_ICON, draftOf, inputOf, minutes, summary } from './rule-draft';
import { RuleEditor } from './rule-editor';

/** La sección Avisos: las reglas del proyecto y el editor de la que se edita. */
@Component({
  selector: 'app-alert-rules',
  imports: [Icon, RuleEditor],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './alert-rules.html',
  styleUrls: ['./settings.css', './alert-rules.css'],
})
export class AlertRules {
  readonly projectId = input.required<number>();

  private readonly api = inject(Api);
  protected readonly i18n = inject(I18n);
  protected readonly t = this.i18n.t;
  protected readonly icons = ACTION_ICON;

  protected readonly rules = signal<Rule[] | null>(null);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );
  /** El id de la regla que se edita, 0 para una nueva o null. */
  protected readonly editing = signal<number | null>(null);
  /** El secreto de webhook recién generado de una regla: llega entero solo al guardarla. */
  protected readonly revealed = signal<{ rule: number; secret: string } | null>(null);
  protected readonly toast = signal('');
  private toastTimer?: ReturnType<typeof setTimeout>;

  protected readonly allOff = computed(() => {
    const rs = this.rules();
    return !!rs?.length && rs.every((r) => !r.enabled);
  });

  protected readonly rows = computed(() => {
    this.i18n.lang();
    return (this.rules() ?? []).map((rule) => ({
      rule,
      summary: summary(rule),
      every: rule.throttle_minutes
        ? this.t('rules.every', { time: minutes(rule.throttle_minutes) })
        : '',
    }));
  });

  constructor() {
    effect(() => {
      const id = this.projectId();
      untracked(() => this.load(id));
    });
    inject(DestroyRef).onDestroy(() => clearTimeout(this.toastTimer));
  }

  protected load(id = this.projectId()): void {
    this.editing.set(null);
    this.failure.set(null);
    this.rules.set(null);
    this.api.rules(id).subscribe({
      next: (page) => this.rules.set(page.data),
      error: (err) => {
        this.failure.set(err);
        this.rules.set([]);
      },
    });
  }

  protected edit(id: number): void {
    this.revealed.set(null);
    this.editing.set(this.editing() === id ? null : id);
  }

  protected toggle(rule: Rule, enabled: boolean): void {
    const input = { ...inputOf(draftOf(rule)), enabled };
    this.api.updateRule(this.projectId(), rule.id, input).subscribe({
      next: (saved) => this.replace(saved),
      error: (err) => {
        this.notify(apiError(err));
        this.rules.update((rs) => (rs ? [...rs] : rs));
      },
    });
  }

  protected saved(rule: Rule): void {
    const fresh = rule.actions.find(
      (a) => a.type === 'webhook' && a.secret && !a.secret.includes('…'),
    );
    this.revealed.set(fresh?.secret ? { rule: rule.id, secret: fresh.secret } : null);
    // Tras enseñarlo una vez, el formulario lo lleva tapado como lo devuelve la API.
    this.replace({
      ...rule,
      actions: rule.actions.map((a) =>
        a === fresh && a.secret ? { ...a, secret: `…${a.secret.slice(-4)}` } : a,
      ),
    });
    this.editing.set(rule.id);
    this.notify(this.t('rules.saved'));
  }

  protected deleted(id: number): void {
    this.rules.update((rs) => (rs ?? []).filter((r) => r.id !== id));
    this.editing.set(null);
    this.notify(this.t('rules.deleted'));
  }

  private replace(rule: Rule): void {
    this.rules.update((rs) => {
      const list = rs ?? [];
      return list.some((r) => r.id === rule.id)
        ? list.map((r) => (r.id === rule.id ? rule : r))
        : [...list, rule];
    });
  }

  private notify(message: string): void {
    clearTimeout(this.toastTimer);
    this.toast.set(message);
    this.toastTimer = setTimeout(() => this.toast.set(''), 2500);
  }
}
