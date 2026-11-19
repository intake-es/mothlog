import { HttpClient, type HttpErrorResponse, type HttpInterceptorFn } from '@angular/common/http';
import { Injectable, inject, signal } from '@angular/core';
import { type CanActivateFn, Router } from '@angular/router';
import { catchError, firstValueFrom, throwError } from 'rxjs';

export type LoginMethod = 'github' | 'dev';

export interface SessionInfo {
  user: { login: string; name: string } | null;
  methods: LoginMethod[];
  org?: string;
}

@Injectable({ providedIn: 'root' })
export class Session {
  private readonly http = inject(HttpClient);
  private readonly router = inject(Router);

  readonly info = signal<SessionInfo | null>(null);
  private loading: Promise<SessionInfo> | null = null;

  /** Lee /auth/session una vez; reload la vuelve a pedir. */
  load(reload = false): Promise<SessionInfo> {
    if (!this.loading || reload) {
      this.loading = firstValueFrom(this.http.get<SessionInfo>('/auth/session'))
        .catch(() => ({ user: null, methods: [] }) as SessionInfo)
        .then((info) => {
          this.info.set(info);
          return info;
        });
    }
    return this.loading;
  }

  /** La sesión caducó a medio uso: se olvida y vuelve al login. */
  expired(): void {
    const current = this.info();
    if (current?.user) {
      this.info.set({ ...current, user: null });
      this.loading = Promise.resolve({ ...current, user: null });
    }
    const next = this.router.url.startsWith('/login') ? undefined : this.router.url;
    void this.router.navigate(['/login'], { queryParams: next ? { next } : {} });
  }

  async logout(): Promise<void> {
    await firstValueFrom(this.http.post('/auth/logout', null)).catch(() => undefined);
    await this.load(true);
    await this.router.navigate(['/login']);
  }
}

export const authGuard: CanActivateFn = async (_route, state) => {
  const session = inject(Session);
  const router = inject(Router);
  const info = await session.load();
  if (info.user) {
    return true;
  }
  return router.createUrlTree(['/login'], {
    queryParams: state.url && state.url !== '/' ? { next: state.url } : {},
  });
};

export const loginGuard: CanActivateFn = async () => {
  const session = inject(Session);
  const router = inject(Router);
  const info = await session.load();
  return info.user ? router.createUrlTree(['/']) : true;
};

export const unauthorizedInterceptor: HttpInterceptorFn = (req, next) => {
  const session = inject(Session);
  return next(req).pipe(
    catchError((err: HttpErrorResponse) => {
      if (err.status === 401 && req.url.startsWith('/api/')) {
        session.expired();
      }
      return throwError(() => err);
    }),
  );
};
