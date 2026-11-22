import { ChangeDetectionStrategy, Component, computed, input, signal } from '@angular/core';
import type { Assignee } from '../core/api';
import { Icon } from './icon';

/** La foto de una persona, su inicial si no hay foto, o un robot si es un token. */
@Component({
  selector: 'app-avatar',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    '[class.avatar--token]': 'isToken()',
    '[class.avatar--inactive]': 'inactive()',
    '[style.--avatar-size.px]': 'size()',
    'aria-hidden': 'true',
  },
  template: `
    @if (isToken()) {
      <svg appIcon="robot"></svg>
    } @else if (photo()) {
      <img [src]="photo()" alt="" referrerpolicy="no-referrer" (error)="broken.set(true)" />
    } @else {
      {{ initial() }}
    }
  `,
  styles: `
    :host {
      --avatar-size: 20px;
      display: inline-flex;
      flex: 0 0 auto;
      align-items: center;
      justify-content: center;
      width: var(--avatar-size);
      height: var(--avatar-size);
      overflow: hidden;
      border-radius: 999px;
      background: var(--bg-hover);
      color: var(--text);
      font-size: calc(var(--avatar-size) * 0.46);
      font-weight: 600;
      line-height: 1;
      user-select: none;
    }
    :host(.avatar--token) {
      border-radius: 6px;
      color: var(--text-muted);
    }
    :host(.avatar--inactive) {
      opacity: 0.5;
    }
    img {
      width: 100%;
      height: 100%;
      object-fit: cover;
    }
    svg {
      width: 62%;
      height: 62%;
    }
  `,
})
export class Avatar {
  readonly who = input.required<Pick<Assignee, 'name' | 'kind'> & Partial<Assignee>>();
  readonly size = input(20);

  protected readonly broken = signal(false);
  protected readonly isToken = computed(() => this.who().kind === 'token');
  protected readonly inactive = computed(() => this.who().active === false);
  protected readonly photo = computed(() => (this.broken() ? '' : (this.who().avatar_url ?? '')));
  protected readonly initial = computed(() =>
    ((this.who().display_name || this.who().name).trim()[0] ?? '?').toUpperCase(),
  );
}
