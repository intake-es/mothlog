import type { Routes } from '@angular/router';
import { authGuard, loginGuard } from './core/session';
import { Shell } from './layout/shell';
import { IssueDetailPage } from './pages/issue-detail/issue-detail';
import { IssueListPage } from './pages/issue-list/issue-list';
import { LoginPage } from './pages/login/login';

export const routes: Routes = [
  {
    path: 'login',
    component: LoginPage,
    canActivate: [loginGuard],
    title: 'Entrar · intake-errors',
  },
  {
    path: '',
    component: Shell,
    canActivate: [authGuard],
    children: [
      { path: '', component: IssueListPage, title: 'Errores · intake-errors' },
      { path: 'issues/:id', component: IssueDetailPage, title: 'Error · intake-errors' },
    ],
  },
  { path: '**', redirectTo: '' },
];
