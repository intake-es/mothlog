import { bootstrapApplication } from '@angular/platform-browser';
import { App } from './app/app';
import { appConfig } from './app/app.config';
import { applyLang, lang } from './app/core/i18n';
import { applyTheme, initialTheme } from './app/core/theme';

applyTheme(initialTheme());
applyLang(lang());

bootstrapApplication(App, appConfig).catch((err) => console.error(err));
