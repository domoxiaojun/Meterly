import { z } from 'zod'

// The center deliberately forbids eval in its CSP, including feature probes.
z.config({ jitless: true })

export { z }
