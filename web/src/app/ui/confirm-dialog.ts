import {
  afterNextRender,
  ChangeDetectionStrategy,
  Component,
  type ElementRef,
  input,
  output,
  viewChild,
} from '@angular/core';

/**
 * Un modal para confirmar algo que no se deshace: el título, el texto y dos
 * botones. Escape o el fondo cancelan; el foco empieza en Cancelar y no sale
 * del modal.
 */
@Component({
  selector: 'app-confirm-dialog',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(keydown.escape)': 'cancel()', '(keydown.tab)': 'trap($event)' },
  template: `
    <div class="veil" aria-hidden="true" (click)="cancel()"></div>
    <div
      class="dialog"
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="confirm-title"
      aria-describedby="confirm-text"
      #box
    >
      <h2 id="confirm-title" class="dialog__title">{{ heading() }}</h2>
      <p id="confirm-text" class="dialog__text">{{ text() }}</p>
      @if (error()) {
        <p class="dialog__error" role="alert">{{ error() }}</p>
      }
      <div class="dialog__actions">
        <button type="button" class="btn" #cancelBtn [disabled]="busy()" (click)="cancel()">
          {{ cancelLabel() }}
        </button>
        <button type="button" class="btn btn--danger" [disabled]="busy()" (click)="confirmed.emit()">
          {{ busy() ? busyLabel() : confirmLabel() }}
        </button>
      </div>
    </div>
  `,
  styles: `
    :host {
      position: fixed;
      inset: 0;
      z-index: 50;
      display: flex;
      align-items: center;
      justify-content: center;
      padding: 1rem;
    }

    .veil {
      position: absolute;
      inset: 0;
      background: light-dark(rgba(20, 20, 19, 0.32), rgba(0, 0, 0, 0.55));
      animation: fade var(--motion-fast) var(--ease-out);
    }

    .dialog {
      position: relative;
      width: min(100%, 26rem);
      padding: 1.25rem 1.25rem 1rem;
      border: 1px solid var(--border);
      border-radius: var(--radius-md);
      background: var(--bg-panel);
      box-shadow: var(--shadow-popover);
      animation: rise var(--motion-base) var(--ease-out);
    }

    .dialog__title {
      color: var(--heading);
      font-size: 15px;
      line-height: 22px;
    }

    .dialog__text {
      margin-top: 0.4rem;
      color: var(--text-muted);
      font-size: 0.8125rem;
      line-height: 1.55;
    }

    .dialog__error {
      margin-top: 0.75rem;
      padding: 0.5rem 0.65rem;
      border-radius: var(--radius-sm);
      background: var(--danger-soft);
      color: var(--danger);
      font-size: 0.8125rem;
    }

    .dialog__actions {
      display: flex;
      justify-content: flex-end;
      gap: 0.5rem;
      margin-top: 1.25rem;
    }

    .btn--danger {
      border-color: transparent;
      background: var(--danger);
      color: #fff;
    }

    .btn--danger:hover:not(:disabled) {
      background: color-mix(in srgb, var(--danger) 88%, #000);
    }

    @keyframes fade {
      from {
        opacity: 0;
      }
    }

    @keyframes rise {
      from {
        opacity: 0;
        transform: translateY(6px) scale(0.98);
      }
    }

    @media (prefers-reduced-motion: reduce) {
      .veil,
      .dialog {
        animation: none;
      }
    }
  `,
})
export class ConfirmDialog {
  readonly heading = input.required<string>();
  readonly text = input.required<string>();
  readonly confirmLabel = input.required<string>();
  readonly cancelLabel = input.required<string>();
  readonly busyLabel = input('');
  readonly busy = input(false);
  readonly error = input('');
  readonly confirmed = output<void>();
  readonly cancelled = output<void>();

  private readonly box = viewChild.required<ElementRef<HTMLElement>>('box');
  private readonly cancelBtn = viewChild.required<ElementRef<HTMLButtonElement>>('cancelBtn');

  constructor() {
    afterNextRender(() => this.cancelBtn().nativeElement.focus());
  }

  protected cancel(): void {
    if (!this.busy()) {
      this.cancelled.emit();
    }
  }

  /** Tab y Mayús+Tab dan la vuelta dentro del modal. */
  protected trap(event: Event): void {
    const buttons = [...this.box().nativeElement.querySelectorAll('button')];
    const first = buttons[0];
    const last = buttons[buttons.length - 1];
    const shift = (event as KeyboardEvent).shiftKey;
    if (!shift && document.activeElement === last) {
      event.preventDefault();
      first?.focus();
    } else if (shift && document.activeElement === first) {
      event.preventDefault();
      last?.focus();
    }
  }
}
