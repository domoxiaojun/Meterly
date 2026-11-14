import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'./tests/browser',workers:1,timeout:30000,use:{baseURL:'http://127.0.0.1:18787',trace:'retain-on-failure'},webServer:{command:'npm run build && go run ./tests/ui-server',url:'http://127.0.0.1:18787/healthz',reuseExistingServer:false,timeout:120000},reporter:[['list']]});
