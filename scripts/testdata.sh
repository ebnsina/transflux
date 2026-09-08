#!/usr/bin/env bash
#
# Generates the media corpus used for manual runs.
#
# Fixtures are generated rather than committed: a repository is a poor place
# for binaries, and a generator says what a file is supposed to be in a way a
# checked-in mp4 never can.
#
# Needs FFmpeg. Version 6 or newer, matching the floor the worker enforces.
set -euo pipefail

out=${1:-testdata}
mkdir -p "$out"

gen() {
  local name=$1; shift
  if [ -f "$out/$name" ]; then
    echo "  $name (already there)"
    return
  fi
  ffmpeg -hide_banner -loglevel error -nostdin -y "$@" "$out/$name"
  echo "  $name"
}

echo "Writing to $out:"

# 4K HDR10: BT.2020 primaries and a PQ transfer, written into the encoder's own
# signalling because the -color_* options alone do not always reach it.
gen 4k_hdr.mp4 \
  -f lavfi -i "testsrc=size=3840x2160:rate=25" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000" -t 4 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
  -x264-params "colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc" \
  -c:a aac -metadata:s:a:0 language=eng -shortest

# Ordinary SDR 1080p.
gen 1080p_sdr.mp4 \
  -f lavfi -i "testsrc2=size=1920x1080:rate=30" \
  -f lavfi -i "sine=frequency=330:sample_rate=48000" -t 4 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac -shortest

# Small enough that most of the ladder cannot be filled.
gen 360p_small.mp4 \
  -f lavfi -i "testsrc=size=640x360:rate=25" \
  -f lavfi -i "sine=frequency=220:sample_rate=48000" -t 3 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac -shortest

# Portrait, to catch anything that assumes landscape.
gen portrait.mp4 \
  -f lavfi -i "testsrc=size=1080x1920:rate=25" -t 3 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p

# Two languages, for multi-track handling.
gen multi_audio.mkv \
  -f lavfi -i "testsrc=size=640x360:rate=25" \
  -f lavfi -i "sine=frequency=440:sample_rate=48000" \
  -f lavfi -i "sine=frequency=880:sample_rate=48000" -t 3 \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p -c:a aac \
  -map 0:v -map 1:a -map 2:a \
  -metadata:s:a:0 language=eng -metadata:s:a:1 language=fra -shortest

# Not media at all, for the paths that must refuse it.
if [ ! -f "$out/corrupt.mp4" ]; then
  head -c 40000 /dev/urandom > "$out/corrupt.mp4"
  echo "  corrupt.mp4"
fi

# A real file cut in half: the encoder was happy, the file is not.
if [ ! -f "$out/truncated.mp4" ]; then
  size=$(wc -c < "$out/4k_hdr.mp4")
  head -c $((size / 2)) "$out/4k_hdr.mp4" > "$out/truncated.mp4"
  echo "  truncated.mp4"
fi

echo "Done."
