# The ask for the Front door's test 5: a draft

A draft for the owner to edit and send to one M team member who has a wired machine outside the house (decisions S13 and S15; the Cutover's step 14b). Nobody sends it for him. Send `fd05-member.sh` with it, as a file.

Before sending: mark the member as M team on People, and run `scripts/cutover/front-door/fd05-outside-transfers --lab --prepare`, which prints the job id that replaces `JOB` below.

---

Hi NAME,

Could you run one test for Omarchy Mac Lab from your place? It measures how fast large files move between a machine outside my house and the lab. I need it from a wired connection that is not mine, and the result decides two of the lab's settings before it opens.

The lab is not open yet. You are the one person let in early, as yourself: nothing of mine goes on your machine.

What it needs: a machine on a wired connection, a shell, `curl` and `bash`, 1 GiB of free disk for the length of the run. It uploads 2 GiB and downloads 1 GiB, so it takes as long as your link needs for that.

1. Open https://lab.omarchy-mac.org and sign in with your GitHub account. I have marked you as M team there.
2. On your own page, https://lab.omarchy-mac.org/me, make a Lab token. Keep it for step 3 and for nothing else; you can revoke it on the same page afterwards.
3. Run the attached file:

       sh fd05-member.sh JOB

   It installs the lab's `lab` command into `~/.local/bin/lab` (no root), then `lab` asks for the Lab token at its own prompt. It takes a Lease on the lab's `m1-pro` in your name, uploads and downloads through the lab's normal path, and releases the Lease. You will see the Lease in the lab's Signal group if you are in it.
4. It ends by printing nine lines between two rules: sizes, seconds and speeds. Send me those lines. They hold no address, no name and no token.

The file is short and plain shell: read it first if you like. It talks to lab.omarchy-mac.org and to nothing else, and deletes its own files when it ends. If you already have Chris's `lab` in `~/.local/bin`, the install stops and changes nothing: tell me and I will send another way.

To undo it afterwards: `rm ~/.local/bin/lab`, and revoke the Lab token on your page.

Thanks,
Marcelo
