import { shallowRef } from 'vue'
import type { EvidenceRow } from './relationships'

// matchNavigation.ts retains the historical perspective chosen from relationship
// evidence. The shell opens a separate detail dialog while leaving the originating
// player page or battle board mounted. A recorded owner and complete match travel
// together so clicking evidence cannot select an unrelated current list row.

// recordedMatch holds a single explicit selection; hovering evidence does not
// change it or invoke a service operation. Closing clears this temporary reference.
export const recordedMatch = shallowRef<EvidenceRow | null>(null)

/** openRecordedMatch selects one evidence row for the shell's contextual match dialog. */
export function openRecordedMatch(row: EvidenceRow): void { recordedMatch.value = row }
