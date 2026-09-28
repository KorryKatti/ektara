# ektara

A terminal music player. It plays local mp3s and YouTube streams, and shows what
you're listening to as a Discord rich presence.

## What it does

- plays `.mp3` files out of a folder on your disk
- searches YouTube and downloads what it finds
- streams from YouTube through a temporary file, which is deleted when the track
  ends
- queues several tracks together, from any mix of those sources
- keeps a history of what you played, in a local SQLite database
- shows the current track in your Discord status

## Requirements

- Go 1.27 or newer
- `ffmpeg`, **which you have to install yourself** — needed for all playback.
  Local files as much as streams, because the audio device is locked to a single
  sample rate for the whole session and ffmpeg is what converts to it. YouTube
  serves Opus and AAC, never mp3, so a stream could not be decoded without it
  anyway.

`ffprobe` ships inside ffmpeg, so it comes with the same install. It is used to
find out how long a track is, and, for a stream, how much of it has arrived so
far.

`yt-dlp` is downloaded and installed automatically the first time it is needed,
so there is nothing to do about that one.

## Build and run

```sh
go build ./...
```

Then run it from a folder that contains the music you want:

```sh
./ektara
```

 **it plays the mp3s in the current directory**, and it keeps its database there too. Running it 
 somewhere else means a different library and an empty history.

## The five modes

The menu asks which of these you want:

| mode | what it does |
| --- | --- |
| 1 | search YouTube, download the result, play the downloaded file |
| 2 | play an mp3 from the current directory |
| 3 | browse your history and play something from it |
| 4 | stream from YouTube, through a temporary file |
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

In the directory you ran it from:

- `songs.db` — your history, plus cover art URLs so repeat plays of the same
  track don't look them up again
- any mp3s you chose to download

And one place outside it, for streams:

- `/tmp/ektara-streams` — the track currently streaming, deleted when it ends.
  Anything left there is from a run that did not exit cleanly, and is cleared on
  the next start.

## Known limits

**A stream is silent for the first few seconds.** YouTube streams are fetched
into a temporary file, and `yt-dlp` spends three to five seconds working out what
to fetch before it writes a single byte. Nothing is wrong when a track starts
quietly.

**Seeking forward past what has downloaded yet waits for the download.** The
audio arrives in the background, and skipping to a place the download has not
reached means waiting there rather than playing silence. A whole YouTube track
usually lands within a few seconds, so this is normally under a second of
nothing. On a long track over a slow connection it is longer than that, and the
position display is the only sign it is happening.

If the download finishes and the place you asked for still is not there, the
player says `seek is past the end of the download` and the track ends, rather
than leaving the position sitting somewhere that will never play.

## Notes

A YouTube stream is downloaded to a temporary file by `yt-dlp` while ffmpeg plays
out of it, rather than read straight off the network. That costs a few seconds of
silence at the start, and buys two things: seeking is a read of a file on disk
instead of a fresh request to YouTube, and it cannot be interrupted by a link
that expired since the track started.

The audio device is opened once, at one sample rate, and cannot be reopened
mid-track. Everything is resampled to that rate by ffmpeg as it plays, so a file
recorded at 44.1kHz sounds right on a 48kHz device. Nothing is decoded in
process.

The player display repaints a whole frame every 100ms and on every key press,
built as one string and written in one go, because a frame assembled from
separate writes shows half of each frame while it is being drawn.

A rewrite of the interface onto [Bubble Tea](https://charm.land/bubbletea/v2)
and [Lip Gloss](https://charm.land/lipgloss/v2) is in progress. The dependencies
are in `go.mod`, but the player display is still drawn by hand, so that part is
not finished.
