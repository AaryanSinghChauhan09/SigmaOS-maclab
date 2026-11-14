#!/bin/bash
# Run on a Mac by a job (lab run MAC --script transfer-file.sh): it leaves
# one file of random bytes among the job's files, for the download half of
# the Front door's transfer tests (5, 15 and 16). 80 MiB: as large as one
# job file may be under labd's 90 MB limit on a body.
set -eu
head -c "${TRANSFER_BYTES:-83886080}" /dev/urandom > "$MACLAB_OUT/transfer.bin"
