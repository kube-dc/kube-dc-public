#!/usr/bin/env python3
"""classify-screen.py — say what a KubeVirt VNC screenshot is showing.

WHY THIS EXISTS: driving Windows Setup past "Press any key to boot from CD or DVD"
is a race. The prompt lasts about five seconds, and a keypress that arrives late
lands on the firmware's "Press any key to enter the Boot Manager Menu" instead,
parking the VM in the edk2 menu — where it reports Running and Ready for hours
while doing nothing. Four of the first five bakes won that race; the fifth lost it
twice and cost four hours.

Rather than tune the timing, press-any-key.sh uses this to CHECK whether it
actually got into Setup, and restarts the VM to try again if it did not. Screen
state is the only honest signal: VMI phase, Ready and AgentConnected all look
identical whether Setup is installing or the firmware is idling in a menu.

Classification is by mean colour, which is enough to separate the states we care
about and needs no OCR:
  setup      — Windows Setup / Windows itself: a saturated blue full screen
  firmware   — edk2 setup or Boot Manager menu: flat mid-grey
  bootprompt — firmware text console: near-black with a little white text
  other      — anything else (still booting, black screen, desktop)

Usage: classify-screen.py <screenshot.png>   -> prints one word, exits 0
"""
import sys

from PIL import Image

def classify(path):
    im = Image.open(path).convert('RGB')
    # Downscale hard: we only want gross colour statistics, and this makes the
    # comparison insensitive to spinners, text and cursor position.
    im = im.resize((64, 64))
    px = list(im.getdata())
    n = len(px)
    r = sum(p[0] for p in px) / n
    g = sum(p[1] for p in px) / n
    b = sum(p[2] for p in px) / n
    brightness = (r + g + b) / 3

    # Setup's background is the Windows blue: blue clearly dominant, and bright
    # enough to be a filled screen rather than a few blue words on black.
    if b > r + 25 and b > 40:
        return 'setup'
    # edk2 menus are flat grey: channels close together, mid brightness.
    if brightness > 60 and max(r, g, b) - min(r, g, b) < 20:
        return 'firmware'
    if brightness < 25:
        return 'bootprompt'
    return 'other'

if __name__ == '__main__':
    if len(sys.argv) != 2:
        sys.exit('usage: classify-screen.py <screenshot.png>')
    print(classify(sys.argv[1]))
