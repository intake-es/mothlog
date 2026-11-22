import type { Routes } from '@angular/router';
import type { Key } from './core/i18n';
import { authGuard, loginGuard } from './core/session';
import { Shell } from './layout/shell';
import { IssueDetailPage } from './pages/issue-detail/issue-detail';
import { IssueListPage } from './pages/issue-list/issue-list';
import { LoginPage } from './pages/login/login';
import { MonitorDetailPage } from './pages/monitor-detail/monitor-detail';
import { MonitorListPage } from './pages/monitor-list/monitor-list';
import { ProjectSettingsPage } from './pages/project-settings/project-settings';
import { ReleaseDetailPage } from './pages/release-detail/release-detail';
import { ReleaseListPage } from './pages/release-list/release-list';
import { UptimeDetailPage } from './pages/uptime-detail/uptime-detail';
import { UptimeFormPage } from './pages/uptime-form/uptime-form';

// Los títulos son claves de las traducciones: los escribe PageTitle en el idioma elegido.
export const routes: Routes = [
  {
    path: 'login',
    component: LoginPage,
    canActivate: [loginGuard],
    title: 'title.login' satisfies Key,
  },
  {
    path: '',
    component: Shell,
    canActivate: [authGuard],
    children: [
      { path: '', component: IssueListPage, title: 'title.list' satisfies Key },
      { path: 'issues/:id', component: IssueDetailPage, title: 'title.issue' satisfies Key },
      { path: 'releases', component: ReleaseListPage, title: 'title.releases' satisfies Key },
      {
        path: 'releases/:project/:version',
        component: ReleaseDetailPage,
        title: 'title.release' satisfies Key,
      },
      { path: 'monitors', component: MonitorListPage, title: 'title.monitors' satisfies Key },
      {
        path: 'monitors/uptime/new',
        component: UptimeFormPage,
        title: 'title.uptimeNew' satisfies Key,
      },
      {
        path: 'monitors/:project/uptime/:slug',
        component: UptimeDetailPage,
        title: 'title.monitor' satisfies Key,
      },
      {
        path: 'monitors/:project/uptime/:slug/edit',
        component: UptimeFormPage,
        title: 'title.uptimeEdit' satisfies Key,
      },
      {
        path: 'monitors/:project/:slug',
        component: MonitorDetailPage,
        title: 'title.monitor' satisfies Key,
      },
      { path: 'settings', component: ProjectSettingsPage, title: 'title.settings' satisfies Key },
      { path: 'projects/:id/settings', redirectTo: 'projects/:id/settings/alerts' },
      {
        path: 'projects/:id/settings/:section',
        component: ProjectSettingsPage,
        title: 'title.settings' satisfies Key,
      },
    ],
  },
  { path: '**', redirectTo: '' },
];
