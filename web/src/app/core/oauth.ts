import { HttpClient } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import type { Observable } from 'rxjs';

/** Los permisos de una aplicación conectada al MCP. */
export type Scope = 'read' | 'write';

export const SCOPES: readonly Scope[] = ['read', 'write'];

/** Una petición de acceso que espera el consentimiento. */
export interface OAuthRequest {
  client_name: string;
  client_id: string;
  /** El client_id es la URL de la ficha de la aplicación. */
  client_url: boolean;
  redirect_uri: string;
  scopes: Scope[];
  expires_at: string;
}

/** Una aplicación conectada: lo que la persona le concedió. */
export interface OAuthGrant {
  id: number;
  client_id: string;
  client_name: string;
  redirect_uri: string;
  scopes: Scope[];
  created_at: string;
  last_used_at: string | null;
  last_used_ip: string | null;
}

@Injectable({ providedIn: 'root' })
export class OAuthApi {
  private readonly http = inject(HttpClient);

  request(id: string): Observable<OAuthRequest> {
    return this.http.get<OAuthRequest>(`/api/0/oauth/requests/${encodeURIComponent(id)}`);
  }

  approve(id: string, scopes: Scope[]): Observable<{ redirect_to: string }> {
    return this.http.post<{ redirect_to: string }>(
      `/api/0/oauth/requests/${encodeURIComponent(id)}/approve`,
      { scopes },
    );
  }

  deny(id: string): Observable<{ redirect_to: string }> {
    return this.http.post<{ redirect_to: string }>(
      `/api/0/oauth/requests/${encodeURIComponent(id)}/deny`,
      null,
    );
  }

  grants(): Observable<{ data: OAuthGrant[] }> {
    return this.http.get<{ data: OAuthGrant[] }>('/api/0/oauth/grants/');
  }

  revoke(id: number): Observable<void> {
    return this.http.delete<void>(`/api/0/oauth/grants/${id}`);
  }
}

/** Salir del panel a otra dirección; en los tests se cambia por uno falso. */
@Injectable({ providedIn: 'root' })
export class Browser {
  go(url: string): void {
    window.location.assign(url);
  }
}
