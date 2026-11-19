# Recording the terminal demo

`scripts/demo.sh` plays a 60 second demo for the landing page. It starts the compose demo, types
each command, runs it for real and shows a read, a sale, a daily cap refusal and a cooldown.

Check the script first without pauses: `DEMO_FAST=1 scripts/demo.sh`.

## Record

Use a terminal about 100 columns by 28 rows with a large font, and stop anything on port 8080.

```bash
brew install asciinema agg          # or: pipx install asciinema
asciinema rec demo.cast --cols 100 --rows 28 -c scripts/demo.sh
agg --font-size 18 demo.cast demo.gif
```

`demo.cast` plays on asciinema.org (`asciinema upload demo.cast`) and `demo.gif` embeds in the README
and in posts. Keep the GIF under 2 MB; if it is larger, lower `--font-size` or `--speed` to 1.2.

Alternative with [VHS](https://github.com/charmbracelet/vhs): a tape that runs `scripts/demo.sh`
and sets `Set Width 1100`, `Set Height 600`, `Set FontSize 18`, then `Sleep 70s`.

## Where it is used

The recording is `site/public/demo.gif` (landing page and README) with `site/public/demo.cast` beside it.
To re-record after the demo changes, run the two commands above and replace both files.
