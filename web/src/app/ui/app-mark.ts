import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';

/**
 * Una aplicación: su inicial en un cuadrado. Sin logos remotos, para que pedir
 * la imagen no avise a otro servidor de quién está mirando.
 */
@Component({
  selector: 'app-mark',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { 'aria-hidden': 'true', '[style.--mark-size.px]': 'size()' },
  template: '{{ initial() }}',
  styles: `
    :host {
      --mark-size: 36px;
      display: inline-flex;
      flex: 0 0 auto;
      align-items: center;
      justify-content: center;
      width: var(--mark-size);
      height: var(--mark-size);
      border: 1px solid var(--border);
      border-radius: calc(var(--mark-size) * 0.28);
      background: var(--bg-surface);
      color: var(--heading);
      font-size: calc(var(--mark-size) * 0.42);
      font-weight: 600;
      line-height: 1;
      user-select: none;
    }
  `,
})
export class AppMark {
  readonly name = input('');
  readonly size = input(36);
  protected readonly initial = computed(() => {
    const first = [...this.name().trim()].find((c) => /[\p{L}\p{N}]/u.test(c));
    return first ? first.toLocaleUpperCase() : '?';
  });
}
