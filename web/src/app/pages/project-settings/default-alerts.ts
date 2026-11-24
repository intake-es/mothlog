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
import { Api, apiError, type DefaultAlerts, type DefaultChannel, type Rule } from '../../core/api';
import { I18n, type Key } from '../../core/i18n';
import { Icon } from '../../ui/icon';
import { ACTION_ICON } from './rule-draft';

interface Channel {
  id: DefaultChannel;
  name: Key;
  hint: Key;
  env: string;
}

const CHANNELS: Channel[] = [
  { id: 'slack', name: 'action.slack', hint: 'defaults.slackHint', env: 'SLACK_WEBHOOK_URL' },
  { id: 'linear', name: 'action.linear', hint: 'defaults.linearHint', env: 'LINEAR_API_KEY' },
];

/** Los interruptores de los canales por defecto, que valen mientras el proyecto no tenga reglas de errores. */
@Component({
  selector: 'app-default-alerts',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './default-alerts.html',
  styleUrls: ['./settings.css', './default-alerts.css'],
})
export class DefaultAlertsSection {
  readonly projectId = input.required<number>();
  /** Las reglas del proyecto, para saber al momento si tiene alguna de errores. */
  readonly rules = input<Rule[] | null>(null);
  readonly notice = output<string>();
  readonly showRules = output<void>();

  private readonly api = inject(Api);
  protected readonly t = inject(I18n).t;
  protected readonly icons = ACTION_ICON;

  protected readonly data = signal<DefaultAlerts | null>(null);
  private readonly failure = signal<unknown>(null);
  protected readonly error = computed(() =>
    this.failure() === null ? '' : apiError(this.failure()),
  );
  protected readonly saving = signal<DefaultChannel | null>(null);

  protected readonly hasRules = computed(() => {
    const rs = this.rules();
    if (rs === null) {
      return this.data()?.has_rules ?? false;
    }
    return rs.some(
      (r) =>
        r.conditions.new ||
        r.conditions.regressed ||
        !!r.conditions.frequency ||
        !!r.conditions.users,
    );
  });

  protected readonly rows = computed(() => {
    const d = this.data();
    if (!d) {
      return [];
    }
    return CHANNELS.map((c) => ({
      ...c,
      ready: d.configured.includes(c.id),
      on: d.configured.includes(c.id) && d[c.id],
    }));
  });

  constructor() {
    effect(() => {
      const id = this.projectId();
      untracked(() => this.load(id));
    });
  }

  protected load(id = this.projectId()): void {
    this.failure.set(null);
    this.data.set(null);
    this.api.defaultAlerts(id).subscribe({
      next: (d) => this.data.set(d),
      error: (err) => this.failure.set(err),
    });
  }

  protected toggle(channel: DefaultChannel, box: HTMLInputElement): void {
    const on = box.checked;
    const before = this.data();
    if (!before) {
      return;
    }
    this.data.set({ ...before, [channel]: on });
    this.saving.set(channel);
    this.api.setDefaultAlerts(this.projectId(), { [channel]: on }).subscribe({
      next: (d) => {
        this.data.set(d);
        this.saving.set(null);
        const name = CHANNELS.find((c) => c.id === channel)?.name;
        this.notice.emit(
          this.t(on ? 'defaults.on' : 'defaults.off', { channel: name ? this.t(name) : channel }),
        );
      },
      error: (err) => {
        this.data.set({ ...before });
        box.checked = before[channel];
        this.saving.set(null);
        this.notice.emit(apiError(err));
      },
    });
  }
}
