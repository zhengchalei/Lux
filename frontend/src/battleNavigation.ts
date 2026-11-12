import { shallowRef } from 'vue'
import type { BattleReview } from './types'

// battleNavigation.ts carries a successful selected-match navigation intent to
// the application shell. The owner and game must match the backend's published
// review before navigation, preventing an earlier roster or a different player's
// perspective from briefly appearing as the user's requested analysis.
//
// The service operation and snapshot reader complete independently, so this small
// reference connects their identities without transferring another roster. The
// originating detail component rejects superseded responses before requesting
// navigation, and the shell clears intent on account replacement. A successful
// match then closes any source dialogs, after the backend no longer needs their
// private profile ownership to validate the chosen lineup.

// requestedBattle contains only navigation identity; loaded player histories stay
// in the existing store and no hover interaction changes this explicit intent.
export const requestedBattle = shallowRef<Pick<BattleReview, 'owner' | 'id'> | null>(null)

/** showReviewedBattle asks the shell to reveal the exact successfully loaded perspective.
 * The caller waits for ReviewMatch before invoking this function. The shell
 * waits for matching published context, then dismisses any source dialogs.
 */
export function showReviewedBattle(owner: string, id: number): void { requestedBattle.value = { owner, id } }
