import { ChangeDetectionStrategy, Component, inject, input, output, signal } from '@angular/core';
import { Api, type IssueLink } from '../../core/api';
import { I18n } from '../../core/i18n';
import { Icon } from '../../ui/icon';

/** El botón que crea a mano la issue de Linear del error y la deja enlazada. */
@Component({
  selector: 'app-linear-create',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './linear-create.html',
  styleUrl: './linear-create.css',
})
export class LinearCreate {
  readonly issueId = input.required<number>();
  readonly created = output<IssueLink>();

  private readonly api = inject(Api);
  protected readonly t = inject(I18n).t;

  protected readonly busy = signal(false);
  protected readonly failed = signal(false);

  protected create(): void {
    this.busy.set(true);
    this.failed.set(false);
    this.api.createLinear(this.issueId()).subscribe({
      next: (link) => {
        this.busy.set(false);
        this.created.emit(link);
      },
      // El detalle de Linear se queda en el registro del servidor.
      error: () => {
        this.busy.set(false);
        this.failed.set(true);
      },
    });
  }
}
