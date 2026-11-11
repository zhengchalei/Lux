<script setup lang="ts">
import { nextTick, onUnmounted, ref, useId } from 'vue'
import AppIcon from './AppIcon.vue'

// InfoHint.vue reveals secondary explanations on pointer hover or keyboard
// focus without reserving permanent space in the workspace. A teleported panel
// escapes clipped scroll containers, including the native inspection dialog's
// top layer when the trigger belongs to that dialog. The trigger keeps an
// accessible name independently from its decorative information icon.
//
// Positioning runs only when opening the panel. Scroll and resize dismiss it
// instead of continuously measuring layout; no mousemove listener or backend
// request participates in the interaction. A generation check prevents a closed
// or unmounted hint from being repositioned after the next render completes.
const props = defineProps<{
  text: string // Secondary explanation to reveal.
  label: string // Accessible trigger action name.
}>()
// Open-state references belong to this trigger; temporary global listeners are
// installed only while its panel is visible and removed before component teardown.
const trigger = ref<HTMLButtonElement>()
const panel = ref<HTMLElement>()
const open = ref(false)
const target = ref<Element | string>('body')
const position = ref({ left: '0px', top: '0px', visibility: 'hidden' as 'hidden' | 'visible' })
const id = useId()
let generation = 0

/** hide closes the panel and removes its temporary viewport dismissal listeners.
 * It also invalidates pending next-render positioning. Callers can safely invoke
 * it repeatedly, including when unmounting an already closed hint trigger.
 */
function hide(): void {
  generation++
  open.value = false
  document.removeEventListener('scroll', hide, true)
  window.removeEventListener('resize', hide)
}

/** show reveals one explanation beside its current trigger after Vue renders it.
 * Calls while open do nothing. The nearest open dialog owns teleported content
 * so browser top-layer rendering and modal focus containment remain effective.
 *
 * Measuring waits for the panel's actual wrapped height before choosing a side.
 * A newer close or open invalidates the request, preventing an obsolete render
 * from changing a later tooltip. Viewport margins clamp the final coordinates,
 * and scrolling dismisses the panel before its anchor can drift elsewhere.
 */
async function show(): Promise<void> {
  if (open.value || !trigger.value) return
  const request = ++generation
  target.value = trigger.value.closest('dialog[open]') || 'body'
  position.value.visibility = 'hidden'
  open.value = true
  await nextTick()
  if (request !== generation || !panel.value || !trigger.value) return
  const anchor = trigger.value.getBoundingClientRect()
  const bounds = panel.value.getBoundingClientRect()
  // Keep explanations away from viewport edges when the trigger sits in a toolbar or compact card corner.
  const margin = 12
  const below = anchor.bottom + 8
  const top = below + bounds.height <= innerHeight - margin ? below : Math.max(margin, anchor.top - bounds.height - 8)
  position.value = { left: `${Math.max(margin, Math.min(anchor.right - bounds.width, innerWidth - bounds.width - margin))}px`, top: `${top}px`, visibility: 'visible' }
  document.addEventListener('scroll', hide, { capture: true, passive: true })
  window.addEventListener('resize', hide, { passive: true })
}

/** leave dismisses pointer hints while keeping a keyboard-focused explanation visible. */
function leave(): void { if (document.activeElement !== trigger.value) hide() }
/** escape dismisses an open hint before allowing a later Escape to close its dialog.
 * Closed hints do not consume the key. Modal inspection therefore retains its
 * native dismissal behavior when keyboard focus stays on an information trigger.
 */
function escape(event: KeyboardEvent): void {
  if (open.value) {
    // Cancel the browser's modal dismissal as well as bubbling so the first Escape closes only the visible explanation.
    event.preventDefault()
    event.stopPropagation()
    hide()
  }
}
onUnmounted(hide)
</script>

<template>
  <button ref="trigger" type="button" class="info-hint" :aria-label="props.label" :aria-describedby="open ? id : undefined" @mouseenter="show" @mouseleave="leave" @focus="show" @blur="hide" @click="show" @keydown.esc="escape"><AppIcon name="info" :size="15" /></button>
  <Teleport :to="target"><span v-if="open" :id="id" ref="panel" class="hint-panel" :style="position" role="tooltip">{{ props.text }}</span></Teleport>
</template>
