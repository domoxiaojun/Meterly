import React from 'react'
import ReactDOM from 'react-dom/client'
import { createHashHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { TooltipProvider } from '@/components/ui/tooltip'
import { parseFilters } from '@/lib/filters'
import App from './app'
import '@fontsource-variable/inter'
import './styles.css'

const root = createRootRoute({ validateSearch: parseFilters, component: App })
const routes = ['/', '/sites', '/sites/$site', '/ips', '/ips/$ip', '/hosts', '/hosts/$host', '/billing', '/system'] as const
const router = createRouter({ routeTree: root.addChildren(routes.map(path => createRoute({ getParentRoute: () => root, path, component: () => null }))), history: createHashHistory(), defaultPreload: 'intent', scrollRestoration: true })
const queryClient = new QueryClient({ defaultOptions: { queries: { refetchOnReconnect: false } } })
ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><QueryClientProvider client={queryClient}><TooltipProvider><RouterProvider router={router} /></TooltipProvider></QueryClientProvider></React.StrictMode>)
