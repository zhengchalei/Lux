import { createApp } from 'vue'
import App from './App.vue'
import DraftCompanion from './views/DraftCompanion.vue'
import './style.css'
import './board.css'
import './history.css'
import './draft.css'
import './review.css'

// main.ts mounts Lux's Vue renderer after loading the application stylesheet.
// The service adapter owns desktop IPC, while components render sanitized state
// and keep mouse hover, selection highlights, and scrolling within the webview.
createApp(new URLSearchParams(location.search).get('panel') === 'draft' ? DraftCompanion : App).mount('#app')
