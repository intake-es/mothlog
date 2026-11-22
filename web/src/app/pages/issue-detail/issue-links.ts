import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import type { IssueLink, LinkState } from '../../core/api';
import { ago, fullDate } from '../../core/format';
import { I18n, type Key } from '../../core/i18n';
import { Icon } from '../../ui/icon';

const STATE_BADGE: Record<LinkState, string> = {
  open: 'sp-badge--gray',
  closed: 'sp-badge--green',
  canceled: 'sp-badge--gray',
  merged: 'sp-badge--blue',
};

const PROVIDER_NAME = { linear: 'Linear', github: 'GitHub' } as const;

interface Row {
  link: IssueLink;
  icon: string;
  name: string;
  kind: Key;
  badge: string;
  state: string;
  /** El nombre del estado allí si dice más que el normalizado: «In Progress». */
  stateName: string;
  updated: string;
  updatedTitle: string;
}

/** Las issues de Linear y las issues y PRs de GitHub del error, con su estado allí. */
@Component({
  selector: 'app-issue-links',
  imports: [Icon],
  changeDetection: ChangeDetectionStrategy.OnPush,
  templateUrl: './issue-links.html',
  styleUrl: './issue-links.css',
})
export class IssueLinks {
  readonly links = input<IssueLink[] | undefined>([]);

  protected readonly t = inject(I18n).t;

  protected readonly rows = computed<Row[]>(() =>
    (this.links() ?? []).map((link) => {
      const state = link.state || null;
      const stateLabel = this.t(state ? (`links.state.${state}` as Key) : 'links.state.unknown');
      return {
        link,
        icon:
          link.provider === 'linear'
            ? 'kanban'
            : link.kind === 'pull_request'
              ? 'git-pull-request'
              : 'github-logo',
        name: link.identifier || link.external_id,
        kind: `links.${link.provider}.${link.kind}` as Key,
        badge: state ? STATE_BADGE[state] : 'sp-badge--gray',
        state: stateLabel,
        stateName:
          link.state_name &&
          link.state_name.toLowerCase() !== (state ?? '') &&
          link.state_name.toLowerCase() !== stateLabel.toLowerCase()
            ? link.state_name
            : '',
        updated: ago(link.updated_at),
        updatedTitle: fullDate(link.updated_at),
      };
    }),
  );

  protected openLabel(r: Row): string {
    return this.t('links.openIn', { name: r.name, provider: PROVIDER_NAME[r.link.provider] });
  }
}
