import { afterAll, afterEach } from 'vitest'

import { installStrictMsw } from './msw'

installStrictMsw({ afterEach, afterAll })
