import { shallowRef } from 'vue'
import type { Player } from './types'
import { recordedMatch } from './matchNavigation'

// playerExplorer.ts connects recorded participant links to the shell's player page.
// A request retains the source perspective and match together with the target's
// public account identifier. The dialog owns loading and its back trail; changing
// this reference never changes the main query or the underlying selected match.

/** ParticipantRequest identifies a clicked recorded account and its exact source match. */
export interface ParticipantRequest {
  owner: string // Source match perspective owner.
  match: number // Source recorded match identifier.
  puuid: string // Public clicked account identifier.
  name: string // Recorded display name hint.
}

// participantRequest holds only the current navigation intent, leaving player
// histories outside the global snapshot and avoiding deep reactive roster copies.
export const participantRequest = shallowRef<ParticipantRequest | null>(null)

/** openParticipant requests a player page for one explicitly identified recorded participant.
 * Missing account or match identities leave the view unchanged. The backend
 * verifies roster membership again before reading the requested player's history.
 */
export function openParticipant(owner: string, match: number, player: Player): void {
  if (owner && match && player.puuid) {
    // Release evidence's upper modal before opening or updating the player page;
    // otherwise a retained explorer would update invisibly beneath that modal.
    recordedMatch.value = null
    participantRequest.value = { owner, match, puuid: player.puuid, name: player.name }
  }
}
