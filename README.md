# ektara

A terminal music player. It plays local mp3s and YouTube streams, and shows
what you're listening to as a Discord rich presence.

## What it does

- plays `.mp3` files out of your library folder
- searches YouTube and downloads what it finds
- streams straight from YouTube, with nothing written to disk
- queues several tracks together, from any mix of those sources
- keeps a history of what you played, in a local SQLite database
- shows the current track in your Discord status
- `asciiart/` renders an image as ASCII text, for cover art or anything else.
  Ported from [go-ascii](https://github.com/nearlynithin/go-ascii), see Credits.

## Requirements

- Go 1.27 or newer, and a C compiler. The build uses cgo, which `go-sqlite3`
  already needed before mpv did, so this is not new.
- **libmpv**, because the audio is played through mpv rather than decoded here.
  It is needed at runtime either way:
  - Debian, Ubuntu: `libmpv2` (runtime) and `libmpv-dev` (build)
  - Arch: `mpv` — one package, which is what provides `libmpv.so`
  - Fedora: `mpv-libs` (runtime) and `mpv-devel` (build)

  If you would rather not have the development headers installed, build with
  `-tags nocgo` instead. That makes the mpv binding load `libmpv.so` at runtime
  with purego, and the headers are then only needed to build, not to run:

  ```sh
  go build -tags nocgo ./...
  ```

  Without that tag the headers are needed at build time, and without cgo at all
  the program will build but then fail on the database, because SQLite needs
  cgo regardless.

- `ffmpeg`, only for mode 1. yt-dlp needs it to turn a video into an mp3.
  Streaming, local files and everything else go through mpv and do not.

`yt-dlp` is downloaded and installed automatically the first time it is needed,
so there is nothing to do about that one.

## Build and run

```sh
./build.sh
```

or, if you would rather not have the size trimming:

```sh
go build ./...
```

Then run it:

```sh
./ektara
```

Your library, your history and the log all live under `~/.local/share/ektara`
and `~/.local/state/ektara`, so the folder you run it from does not matter.
Point `XDG_DATA_HOME` somewhere else if you want the library on another disk.

## The five modes

The menu asks which of these you want:

| mode | what it does |
| --- | --- |
| 1 | search YouTube, download the result, play the downloaded file |
| 2 | play an mp3 from your library |
| 3 | browse your history and play something from it |
| 4 | stream from YouTube, without writing anything to disk |
| 5 | build a queue out of several tracks, which can mix all of the above |

Modes 1 to 4 hand back a single track. Only mode 5 gives you more than one, so
`a` and `d` only move between tracks when you are in a queue.

## Keys

While a track is playing:

| key | what it does |
| --- | --- |
| `p` | pause and unpause |
| `h` / `k` | seek back / forward five seconds |
| `a` / `d` | previous / next track |
| `s` | shuffle the queue |
| `r` | repeat the queue |
| `m` | mute |
| `+` / `-` | volume |
| `q` | back to the menu |

Shuffle and repeat are keys rather than menu settings because they are nice to
flip in the middle of listening. They hold for the rest of the session, so a
queue you shuffled stays shuffled next time.

While browsing history, the keys are different:

| key | what it does |
| --- | --- |
| `a` / `d` | older / newer |
| `enter` | play it |
| `q` or `esc` | cancel |

## What gets written to disk

In your data directory, `~/.local/share/ektara`:

- `songs.db` — your history, and a cache of searches so a repeated lookup does
  not ask YouTube again
- `music/` — the library: any mp3s you chose to download, plus your own

In your state directory, `~/.local/state/ektara`:

- `ektara.log`

A stream writes nothing. It used to go through a temporary file that was deleted
when the track ended, and there is none of that left to clean up.

## Known limits

**A stream takes a few seconds to start.** Nothing is wrong when a track begins
quietly. `yt-dlp` has to work out a direct link to the audio before there is
anything to play, which takes three to five seconds. Everything after that is
mpv's own buffering and is not noticeable.

**A local file's length is only known once mpv has opened it.** The progress bar
has nothing to draw against for the first moment of a track, then settles.

**YouTube is not always going to answer.** If it decides you have asked for too
much, a search falls back on an older cached answer rather than reporting the
refusal, and a stream that will not resolve is reported as a failed track and
skipped. A request limiter spaces out searches and stream resolutions for this
reason, though it is deliberately loose: ten at once, then one every five
seconds.

## Notes

**Playback is mpv's job, not this program's.** It opens the sound device,
decodes, resamples to whatever the device wants, and seeks without being
restarted. Nothing is decoded in process, which is why the local-file mode
plays a 44.1kHz recording at the right speed on a 48kHz device without anything
here converting it.

**Streams are a url, not a file.** `yt-dlp` resolves a direct media link and mpv
plays that. The link is good for hours and a track is minutes. This replaced a
temporary file that was downloaded while ffmpeg read it, and the code to keep
those two in step was the largest part of the program.

**There is one player for the whole session.** The audio device is slow to open,
so a track change loads a new file onto the same mpv instance rather than making
a new one, and there is no gap of silence between tracks.

**The player display repaints a whole frame every 100ms and on every key press,
built as one string and written in one go, because a frame assembled from
separate writes shows half of each frame while it is being drawn.**

## Tests

```sh
go test ./...                        # everything that needs nothing special
EKTARA_AUDIO_TESTS=1 go test ./...   # also plays real audio through the device
```

The audio tests are skipped unless you ask for them, because a machine with no
sound server would fail them for reasons that have nothing to do with the code.
One further test goes to YouTube and needs `EKTARA_NETWORK_TESTS=1` as well.

Those tests are worth running before a change to the player. They are the only
thing that can say the audio path works at all, and they have caught a deadlock
in seeking and a use-after-free at shutdown that nothing else could see.

## Credits

**`asciiart/` is a port of [go-ascii](https://github.com/nearlynithin/go-ascii)
by [nearlynithin](https://github.com/nearlynithin).** The sampling, the
luminosity and the character ramp are theirs. The original is a `main` package
that writes to stdout and reads the terminal size itself; this is the same
conversion as a library that returns a string and takes the width as an
argument, so it can be drawn inside a TUI.

There are two ways to draw an image with it. `Mode: Mono` is the original: one
ramp character per pixel, which survives being pasted anywhere. `Mode: HalfBlock`
packs two vertically adjacent pixels into every cell instead, putting the top one
in the foreground colour and the bottom one in the background behind a `▀`. That
doubles the vertical resolution at the same width and is what makes the colour
worth having.

```go
// plain text, the original's conversion
asciiart.Render(img, asciiart.Options{Width: 80})

// colour, two pixels per cell
asciiart.Render(img, asciiart.Options{Width: 80, Color: true, Mode: asciiart.HalfBlock})
```

Half blocks only render where the terminal draws them, and need a terminal that
does truecolour. Adjacent cells that share a colour are styled once rather than
once each, which is the difference between a frame of tens of kilobytes and a few.

If you want the original tool rather than the library:

```sh
git clone https://github.com/nearlynithin/go-ascii
cd go-ascii && go build -o go-ascii
./go-ascii [-color] <image.png>
```

