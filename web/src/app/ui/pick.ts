import { ChangeDetectionStrategy, Component, computed, input, output, signal } from '@angular/core';
import { Icon } from './icon';

export interface PickOption {
  value: string;
  label: string;
}

/** El filtro en píldora: icono de qué filtra, el valor elegido y un menú. */
@Component({
  selector: 'app-pick',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(keydown.escape)': 'open.set(false)' },
  template: `
    <button
      type="button"
      class="pick"
      [class.pick--on]="highlight()"
      [attr.aria-label]="label() + ': ' + current()"
      aria-haspopup="menu"
      [attr.aria-expanded]="open()"
      (click)="open.set(!open())"
    >
      <svg [appIcon]="icon()"></svg>
      <span class="pick__value">{{ current() }}</span>
      <svg appIcon="caret-down" class="pick__caret"></svg>
    </button>
    @if (open()) {
      <button type="button" class="menu__veil" tabindex="-1" aria-hidden="true" (click)="open.set(false)"></button>
      <div class="menu" [class.menu--end]="align() === 'end'" role="menu" [attr.aria-label]="label()">
        <div class="menu__label">{{ label() }}</div>
        @for (o of options(); track o.value) {
          <button type="button" class="menu__item" role="menuitemradio" [attr.aria-checked]="o.value === value()" (click)="choose(o.value)">
            {{ o.label }}
            @if (o.value === value()) {
              <svg appIcon="check" class="menu__check"></svg>
            }
          </button>
        }
      </div>
    }
  `,
  styles: `
    :host {
      position: relative;
      display: inline-flex;
    }
    .pick {
      display: inline-flex;
      align-items: center;
      gap: 0.4rem;
      height: var(--control-h);
      max-width: 15rem;
      padding: 0 0.6rem 0 0.7rem;
      border: 1px solid var(--border-strong);
      border-radius: var(--radius-pick);
      background: var(--bg-panel);
      color: var(--text);
      font-size: 0.8125rem;
      font-weight: 500;
      transition:
        background-color var(--motion-fast) var(--ease-out),
        border-color var(--motion-fast) var(--ease-out);
    }
    .pick:hover {
      background: var(--bg-hover);
    }
    .pick svg {
      flex: 0 0 auto;
      width: 15px;
      height: 15px;
      color: var(--text-muted);
    }
    .pick__value {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pick .pick__caret {
      width: 12px;
      height: 12px;
    }
    .pick--on {
      border-color: var(--text-muted);
      background: var(--bg-hover);
    }
    .menu {
      top: calc(100% + 6px);
      left: 0;
    }
    .menu--end {
      right: 0;
      left: auto;
    }
  `,
})
export class Pick {
  readonly icon = input.required<string>();
  readonly label = input.required<string>();
  readonly options = input.required<PickOption[]>();
  readonly value = input.required<string>();
  readonly align = input<'start' | 'end'>('start');
  /** Más oscuro cuando el filtro no está en su valor por defecto. */
  readonly highlight = input(false);
  readonly changed = output<string>();

  protected readonly open = signal(false);

  protected readonly current = computed(
    () => this.options().find((o) => o.value === this.value())?.label ?? this.value(),
  );

  protected choose(value: string): void {
    this.open.set(false);
    if (value !== this.value()) {
      this.changed.emit(value);
    }
  }
}
