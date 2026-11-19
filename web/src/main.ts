import { bootstrapApplication } from '@angular/platform-browser';
import { App } from './app/app';
import { appConfig } from './app/app.config';
import { applyTheme, initialTheme } from './app/core/theme';

applyTheme(initialTheme());

bootstrapApplication(App, appConfig).catch((err) => console.error(err));
