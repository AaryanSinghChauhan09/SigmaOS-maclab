# The ask for the Alias: a draft

A draft for the owner to edit and send to Emir, who configures DNS on `omarchy.org` (spec section 6, "The Alias"; the Cutover's step 35a). Nobody sends it for him, and nothing waits on the answer.

Send it only when both are true:

- the lab is Confirmed (step 34);
- the Alias rehearsal is green: `docs/acceptance/` holds an `alias-rehearsal` record that ends in `Result: pass`.

The record asked for is `maclab` as a CNAME of `lab.omarchy-mac.org`. The rehearsal proved exactly that record on a throwaway name. If its run showed Cloudflare wants another target, change the target below before sending.

---

Hi Emir,

The Omarchy Mac Lab is live at lab.omarchy-mac.org: the hardware-test lab and the hardware report site in one place. I'd like maclab.omarchy.org to lead people there.

Could you add one DNS record to omarchy.org?

    maclab   CNAME   lab.omarchy-mac.org   proxy status: DNS only (grey cloud)

That's the whole request. The name only forwards visitors to lab.omarchy-mac.org. Cloudflare issues and renews its certificate on my side; no access to your account and no tokens are involved. It needs to stay DNS only, and deleting the record turns it off at any time. If you ever plan to remove it, a heads-up would be appreciated.

If you'd rather not, no problem: the lab works without it.

Thanks,
Marcelo

---

## After the answer

**Yes.** That day, not earlier (a custom hostname left waiting for its record lapses in about three weeks):

1. A short-lived Cloudflare token in a file only you read: `scripts/cloud/owner-setup.sh` (Zone: Read; SSL and Certificates: Edit).
2. `scripts/cloud/alias.sh --apply --token-file FILE` makes the custom hostname `maclab.omarchy.org` on the lab's zone.
3. Tell Emir it is ready for the record.
4. Once he has added it: `scripts/cloud/alias.sh --check --token-file FILE` until it exits 0 (the hostname and its certificate active), then open `https://maclab.omarchy.org/reports` in a browser: it lands on the same page at the Lab address.
5. Set `"wanted": true` under `alias` in `scripts/cloud/edge.json` and commit: from then on `scripts/cloud/zone-settings.sh --check` fails if the Alias drifts. labd on the mini already forwards the name and tells "Agents" once if it stops leading to the lab.

**Silent.** One reminder after 14 days, then dropped.

**No.** Dropped. If they would rather make a redirect in their own zone, that is accepted and needs nothing from the lab.

**The record is removed later.** Accepted: nobody is chased and nothing is made again. "Agents" gets one line; Cloudflare deletes the custom hostname seven days later. Set `"wanted": false` in `scripts/cloud/edge.json` and commit.
