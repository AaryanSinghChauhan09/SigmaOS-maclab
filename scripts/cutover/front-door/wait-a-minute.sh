#!/bin/bash
# Run on a Mac by a job (lab run MAC --script wait-a-minute.sh): it takes a
# minute and changes nothing, so that the Front door's test 11 has a job
# that is still running when its waiter is cut.
set -eu
sleep "${WAIT_SECONDS:-60}"
echo waited
