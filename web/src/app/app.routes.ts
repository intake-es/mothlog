import type { Routes } from '@angular/router';
import type { Key } from './core/i18n';
import { authGuard, loginGuard } from './core/session';
import { Shell } from './layout/shell';

// Los títulos son claves de las traducciones: los escribe PageTitle en el idioma elegido.
export const routes: Routes = [
  {
    path: 'login',
    loadComponent: () => import('./pages/login/login').then((m) => m.LoginPage),
    canActivate: [loginGuard],
    title: 'title.login' satisfies Key,
  },
  {
    path: '',
    component: Shell,
    canActivate: [authGuard],
    children: [
      {
        path: '',
        loadComponent: () => import('./pages/issue-list/issue-list').then((m) => m.IssueListPage),
        title: 'title.list' satisfies Key,
      },
      {
        path: 'issues/:id',
        loadComponent: () =>
          import('./pages/issue-detail/issue-detail').then((m) => m.IssueDetailPage),
        title: 'title.issue' satisfies Key,
      },
      {
        path: 'releases',
        loadComponent: () =>
          import('./pages/release-list/release-list').then((m) => m.ReleaseListPage),
        title: 'title.releases' satisfies Key,
      },
      {
        path: 'releases/:project/:version',
        loadComponent: () =>
          import('./pages/release-detail/release-detail').then((m) => m.ReleaseDetailPage),
        title: 'title.release' satisfies Key,
      },
      {
        path: 'monitors',
        loadComponent: () =>
          import('./pages/monitor-list/monitor-list').then((m) => m.MonitorListPage),
        title: 'title.monitors' satisfies Key,
      },
      {
        path: 'monitors/uptime/new',
        loadComponent: () =>
          import('./pages/uptime-form/uptime-form').then((m) => m.UptimeFormPage),
        title: 'title.uptimeNew' satisfies Key,
      },
      {
        path: 'monitors/:project/uptime/:slug',
        loadComponent: () =>
          import('./pages/uptime-detail/uptime-detail').then((m) => m.UptimeDetailPage),
        title: 'title.monitor' satisfies Key,
      },
      {
        path: 'monitors/:project/uptime/:slug/edit',
        loadComponent: () =>
          import('./pages/uptime-form/uptime-form').then((m) => m.UptimeFormPage),
        title: 'title.uptimeEdit' satisfies Key,
      },
      {
        path: 'monitors/:project/:slug',
        loadComponent: () =>
          import('./pages/monitor-detail/monitor-detail').then((m) => m.MonitorDetailPage),
        title: 'title.monitor' satisfies Key,
      },
      {
        path: 'settings',
        loadComponent: () =>
          import('./pages/project-settings/project-settings').then((m) => m.ProjectSettingsPage),
        title: 'title.settings' satisfies Key,
      },
      { path: 'projects/:id/settings', redirectTo: 'projects/:id/settings/alerts' },
      {
        path: 'projects/:id/settings/:section',
        loadComponent: () =>
          import('./pages/project-settings/project-settings').then((m) => m.ProjectSettingsPage),
        title: 'title.settings' satisfies Key,
      },
    ],
  },
  { path: '**', redirectTo: '' },
];
