#!/usr/bin/env python3
"""Make MP4 from actual PTY events, preserving event order at declared speed.
Dependencies: Pillow==11.3.0 pyte==0.8.2 av==15.1.0
Usage: python render.py input.cast output.mp4 [speed=4]
"""
import json
import sys
from pathlib import Path
import av
import pyte
from PIL import Image, ImageDraw, ImageFont

source, output = map(Path, sys.argv[1:3])
speed = float(sys.argv[3]) if len(sys.argv) > 3 else 4.0
rows = [json.loads(line) for line in source.read_text().splitlines()]
header, events = rows[0], rows[1:]
screen = pyte.Screen(header['width'], header['height'])
terminal = pyte.Stream(screen)
font = ImageFont.truetype('/System/Library/Fonts/Menlo.ttc', 17)
fps, width, height = 2, 1440, 960
container = av.open(str(output), mode='w')
stream = container.add_stream('libx264', rate=fps)
stream.width, stream.height, stream.pix_fmt = width, height, 'yuv420p'
stream.options = {'crf': '24', 'preset': 'fast'}
duration = events[-1][0] / speed + 3
index = 0
for n in range(int(duration * fps) + 1):
    moment = n / fps * speed
    while index < len(events) and events[index][0] <= moment:
        if events[index][1] == 'o':
            terminal.feed(events[index][2])
        index += 1
    im = Image.new('RGB', (width, height), '#0c1118')
    draw = ImageDraw.Draw(im)
    draw.text((24, 18), f'REAL PTY RECORDING | {source.name} | {speed:g}x | source t={int(moment)}s', font=font, fill='#66d9cb')
    draw.text((24, 48), 'Replay of captured terminal output; no desktop capture or synthetic test output.', font=font, fill='#b6bdc8')
    for line, text in enumerate(screen.display):
        draw.text((24, 92 + line * 21), text.rstrip(), font=font, fill='#e7ecf1')
    frame = av.VideoFrame.from_image(im)
    for packet in stream.encode(frame):
        container.mux(packet)
for packet in stream.encode():
    container.mux(packet)
container.close()
print(f'{output.name}: {duration:.1f}s at {speed:g}x, {output.stat().st_size} bytes')
