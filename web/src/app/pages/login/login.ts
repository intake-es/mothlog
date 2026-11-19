import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { Session } from '../../core/session';
import { Icon } from '../../ui/icon';

const ERRORS: Record<string, string> = {
  org: 'Tu cuenta de GitHub no es miembro activo de la organización. Pide acceso a quien la administra.',
  state: 'El intento de entrar caducó o no venía de aquí. Vuelve a probar.',
  denied: 'Has cancelado el acceso en GitHub.',
  github: 'GitHub no ha respondido como se esperaba. Vuelve a probar en un momento.',
  config: 'El login con GitHub no está configurado en este servidor.',
};

@Component({
  selector: 'app-login',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './login.html',
  styleUrl: './login.css',
})
export class LoginPage {
  readonly error = input<string>();
  readonly next = input<string>();

  private readonly session = inject(Session);

  protected readonly info = this.session.info;
  protected readonly loaded = computed(() => this.info() !== null);
  protected readonly github = computed(() => this.info()?.methods.includes('github') ?? false);
  protected readonly dev = computed(() => this.info()?.methods.includes('dev') ?? false);
  protected readonly configured = computed(() => this.github() || this.dev());
  protected readonly message = computed(() => {
    const e = this.error();
    return e ? (ERRORS[e] ?? 'No se ha podido entrar. Vuelve a probar.') : '';
  });

  constructor() {
    void this.session.load();
  }

  protected href(path: string): string {
    const next = this.next();
    return next ? `${path}?next=${encodeURIComponent(next)}` : path;
  }
}
